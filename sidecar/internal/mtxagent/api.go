// Package mtxagent contains the narrow control-plane agent that can run beside a remote MediaMTX process.
// It relays only operations in the pinned MediaMTX Control API table; it is not a general HTTP proxy or remote shell.
package mtxagent

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mtxui/internal/mtxapi"
	"mtxui/internal/pathname"
)

const (
	apiPrefix      = "/v1/media"
	maxAPIRequest  = 4 << 20
	maxAPIResponse = 32 << 20
)

var (
	pageQuery = regexp.MustCompile(`^\d{1,5}$`)
	apiUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type apiRoute struct {
	op       mtxapi.Operation
	literals []string
	param    string
	query    map[string]bool
}

// API relays a deliberately limited set of MediaMTX Control API operations over an authenticated transport.
type API struct {
	upstream string
	client   *http.Client
	routes   []apiRoute
}

// NewAPI builds the relay. upstream must be an explicit local HTTP(S) MediaMTX API URL, not a request value.
func NewAPI(upstream string, client *http.Client) (*API, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("agent MediaMTX API URL must be an origin without credentials or a path")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	ops, err := mtxapi.Operations()
	if err != nil {
		return nil, err
	}
	a := &API{upstream: strings.TrimSuffix(upstream, "/"), client: client}
	for _, op := range ops {
		if op.Access == mtxapi.AccessUnused {
			continue
		}
		route := apiRoute{op: op, query: map[string]bool{}}
		for _, segment := range strings.Split(strings.TrimPrefix(op.Path, "/"), "/") {
			if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
				route.param = strings.Trim(segment, "{}")
				break
			}
			route.literals = append(route.literals, segment)
		}
		for _, key := range op.Query {
			route.query[key] = true
		}
		a.routes = append(a.routes, route)
	}
	return a, nil
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete && r.Method != http.MethodPatch && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Run mounts this handler only behind the agent's mutual-TLS listener, whose peer certificate is pinned to the
	// management application's identity. Preserve an absent Authorization header so the central safety probe can
	// verify that MediaMTX itself rejects anonymous Control API access.
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if !strings.HasPrefix(r.URL.Path, apiPrefix+"/") {
		http.NotFound(w, r)
		return
	}
	route, param, found := a.match(r.Method, path)
	if route == nil {
		if found {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
		return
	}
	if !validQuery(route, r.URL.Query()) {
		http.Error(w, "unsupported query parameters", http.StatusBadRequest)
		return
	}
	if route.param != "" {
		if err := validParameter(route.param, param); err != nil {
			http.Error(w, "invalid API path parameter", http.StatusBadRequest)
			return
		}
	}
	if r.ContentLength > maxAPIRequest {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIRequest+1))
	if err != nil || len(body) > maxAPIRequest {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	targetPath := route.op.Path
	if route.param != "" {
		parts := strings.Split(param, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		targetPath = strings.TrimSuffix(targetPath, "{"+route.param+"}") + strings.Join(parts, "/")
	}
	target := a.upstream + targetPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, strings.NewReader(string(body))) //nolint:gosec // target uses the configured API origin and a pinned, validated operation path
	if err != nil {
		http.Error(w, "invalid API request", http.StatusBadRequest)
		return
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	for _, key := range []string{"Accept", "Content-Type"} {
		if value := r.Header.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := a.client.Do(req) //nolint:gosec // redirects are disabled and the destination is the fixed API origin
	if err != nil {
		http.Error(w, "MediaMTX Control API is unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	if value := resp.Header.Get("Content-Type"); value != "" {
		w.Header().Set("Content-Type", value)
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil || len(response) > maxAPIResponse {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "MediaMTX Control API response too large\n")
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(response)
}

func (a *API) match(method, path string) (*apiRoute, string, bool) {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	found := false
	for i := range a.routes {
		route := &a.routes[i]
		n := len(route.literals)
		if route.param == "" && len(segments) != n || route.param != "" && len(segments) < n+1 {
			continue
		}
		matched := true
		for j, literal := range route.literals {
			if segments[j] != literal {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		found = true
		if route.op.Method == method {
			return route, strings.Join(segments[n:], "/"), true
		}
	}
	return nil, "", found
}

func validQuery(route *apiRoute, query url.Values) bool {
	for key, values := range query {
		if !route.query[key] || len(values) != 1 || len(values[0]) > 256 {
			return false
		}
		if (key == "page" || key == "itemsPerPage") && !pageQuery.MatchString(values[0]) {
			return false
		}
	}
	return true
}

func validParameter(name, value string) error {
	switch name {
	case "name":
		return pathname.Valid(value)
	case "id":
		if !apiUUID.MatchString(value) {
			return errors.New("invalid id")
		}
	default:
		return errors.New("unknown parameter")
	}
	return nil
}
