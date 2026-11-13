// Package probe watches MediaMTX from the sidecar: whether it answers, its version, and whether its Control API
// refuses anonymous requests. The last check is the startup safety check: while the API answers anonymously, the
// sidecar refuses to serve. The probe also collects warnings from elsewhere (a reverse proxy
// that does not match PUBLIC_URL, never-published ports that answer at PUBLIC_HOST) for the status endpoint.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Warning is something an admin should fix.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Status is the probe's latest view.
type Status struct {
	CheckedAt              time.Time `json:"checkedAt"`
	Reachable              bool      `json:"reachable"` // MediaMTX Control API
	Version                string    `json:"version"`
	ExpectedVersion        string    `json:"expectedVersion"`
	APIProtected           *bool     `json:"apiProtected"` // nil until known
	Unsafe                 string    `json:"unsafe,omitempty"`
	Failure                string    `json:"failure,omitempty"` // management_unavailable or media_api_unavailable
	ManagementReachable    *bool     `json:"managementReachable,omitempty"`
	ManagementVersion      string    `json:"managementVersion,omitempty"`
	ManagementCapabilities []string  `json:"managementCapabilities,omitempty"`
	Warnings               []Warning `json:"warnings"`
}

// Authorizer adds the sidecar's credentials to a request.
type Authorizer interface {
	Authorize(*http.Request)
}

// Prober checks MediaMTX periodically.
type Prober struct {
	api              string
	managementHealth string
	principal        Authorizer
	expected         string
	publicHost       string
	client           *http.Client
	dial             func(ctx context.Context, network, addr string) (net.Conn, error)
	lookup           func(ctx context.Context, host string) ([]netip.Addr, error)
	log              *slog.Logger

	// StackSubnet is the stack network's (settings.StackSubnet). A PUBLIC_HOST there is no test of publishing.
	StackSubnet netip.Prefix
	// SkipExposureCheck is set when a remote agent is the private Control API transport; PUBLIC_HOST belongs to the media host.
	SkipExposureCheck bool

	mu       sync.RWMutex
	status   Status
	warnings map[string]Warning
}

// neverPublished are MediaMTX's ports that must not answer from outside, with what they are.
var neverPublished = []struct {
	port int
	what string
}{{9997, "Control API"}, {9998, "metrics"}, {9996, "playback"}, {8888, "HLS"}, {8889, "WebRTC signalling"}}

// New returns a prober. expected is the MediaMTX version the sidecar was built for, e.g. "1.21.1".
func New(api string, principal Authorizer, expected, publicHost string, log *slog.Logger) *Prober {
	d := &net.Dialer{Timeout: 2 * time.Second}
	return &Prober{
		api: strings.TrimSuffix(api, "/"), principal: principal, expected: expected, publicHost: publicHost,
		client: &http.Client{Timeout: 5 * time.Second}, dial: d.DialContext, log: log,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		status: Status{ExpectedVersion: expected}, warnings: map[string]Warning{},
	}
}

// Run checks every 2 s until the first definite answer, then every 30 s, and probes the public ports every
// 10 minutes. It returns when ctx ends.
func (p *Prober) Run(ctx context.Context) {
	var lastExposure time.Time
	mediaSeen := false
	for {
		p.Check(ctx)
		if p.Status().Reachable {
			mediaSeen = true
		}
		if mediaSeen && time.Since(lastExposure) > 10*time.Minute {
			p.CheckExposure(ctx)
			lastExposure = time.Now()
		}
		wait := 30 * time.Second
		if p.Status().APIProtected == nil {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Check asks MediaMTX's API for its paths without credentials (must be refused) and for its version with them.
func (p *Prober) Check(ctx context.Context) {
	st := Status{CheckedAt: time.Now(), ExpectedVersion: p.expected}
	if p.managementHealth != "" {
		ok, version, capabilities := p.checkManagement(ctx)
		st.ManagementReachable = &ok
		st.ManagementVersion = version
		st.ManagementCapabilities = capabilities
		if !ok {
			st.Failure = "management_unavailable"
			p.updateStatus(st)
			return
		}
	}
	code, _, err := p.get(ctx, "/v3/paths/list", false)
	if err == nil && (code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusOK) {
		st.Reachable = true
		switch code {
		case http.StatusUnauthorized:
			t := true
			st.APIProtected = &t
		case http.StatusOK:
			f := false
			st.APIProtected = &f
			st.Unsafe = "MediaMTX answers Control API requests without credentials. The sidecar refuses to serve until " +
				"MediaMTX's authentication is fixed (authMethod must be http, pointing at the sidecar)."
		}
		if _, body, err := p.get(ctx, "/v3/info", true); err == nil {
			var info struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(body, &info) == nil {
				st.Version = strings.TrimPrefix(info.Version, "v")
			}
		}
	} else {
		st.Failure = "media_api_unavailable"
	}

	p.updateStatus(st)
}

func (p *Prober) updateStatus(st Status) {
	p.mu.Lock()
	prev := p.status
	st.Warnings = nil
	p.status = st
	switch {
	case st.Version != "" && st.Version != p.expected:
		p.warnings["version"] = Warning{"version", fmt.Sprintf("MediaMTX is version %s, but this MediaMTXUI release was built for %s. Confirm that the deployed MediaMTX version is compatible with this release.", st.Version, p.expected)}
	default:
		delete(p.warnings, "version")
	}
	p.mu.Unlock()

	if st.Unsafe != "" && prev.Unsafe == "" {
		p.log.Error("startup safety check failed: refusing to serve", "reason", st.Unsafe)
	}
	if st.Unsafe == "" && prev.Unsafe != "" {
		p.log.Info("MediaMTX refuses anonymous API requests again; serving")
	}
	if st.Reachable != prev.Reachable {
		p.log.Info("MediaMTX reachability changed", "reachable", st.Reachable)
	}
}

func (p *Prober) checkManagement(ctx context.Context) (bool, string, []string) {
	code, body, err := p.getURL(ctx, p.managementHealth, false)
	if err != nil || code != http.StatusOK {
		return false, "", nil
	}
	var health struct {
		Status       string   `json:"status"`
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(body, &health) != nil || health.Status != "ok" {
		return false, "", nil
	}
	return true, health.Version, health.Capabilities
}

// CheckExposure warns about each never-published MediaMTX port that accepts TCP connections at PUBLIC_HOST. From
// inside the stack this cannot prove a port is closed to the internet, so it only ever warns. A PUBLIC_HOST on the
// stack's own network or on loopback (the e2e stack names the mediamtx container) reaches those ports whether or not
// anything is published, so there is nothing to check.
func (p *Prober) CheckExposure(ctx context.Context) {
	if p.SkipExposureCheck {
		for _, np := range neverPublished {
			p.Clear("exposed_" + strconv.Itoa(np.port))
		}
		return
	}
	inside := p.insideStack(ctx)
	for _, np := range neverPublished {
		code := "exposed_" + strconv.Itoa(np.port)
		if inside {
			p.Clear(code)
			continue
		}
		conn, err := p.dial(ctx, "tcp", net.JoinHostPort(p.publicHost, strconv.Itoa(np.port)))
		if err != nil {
			p.Clear(code)
			continue
		}
		_ = conn.Close()
		p.Warn(code, fmt.Sprintf("Port %d (MediaMTX %s) accepts connections at %s. It must never be published.", np.port, np.what, p.publicHost))
	}
}

// SetHTTPClient supplies the authenticated agent transport for remote health checks. Set it before Run.
func (p *Prober) SetHTTPClient(client *http.Client) {
	if client != nil {
		p.client = client
	}
}

// SetManagementHealthURL enables an independent health check of the remote management transport.
func (p *Prober) SetManagementHealthURL(rawURL string) {
	p.managementHealth = strings.TrimSuffix(rawURL, "/")
}

// insideStack reports whether PUBLIC_HOST names an address on loopback or the stack's own network. A name that does
// not resolve is not inside: the dial then fails, and nothing warns.
func (p *Prober) insideStack(ctx context.Context) bool {
	addrs := []netip.Addr{}
	if a, err := netip.ParseAddr(p.publicHost); err == nil {
		addrs = append(addrs, a)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addrs, _ = p.lookup(ctx, p.publicHost)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if a.IsLoopback() || (p.StackSubnet.IsValid() && p.StackSubnet.Contains(a)) {
			return true
		}
	}
	return false
}

func (p *Prober) get(ctx context.Context, path string, authorize bool) (int, []byte, error) {
	return p.getURL(ctx, p.api+path, authorize)
}

func (p *Prober) getURL(ctx context.Context, target string, authorize bool) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	if authorize {
		p.principal.Authorize(req)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// Warn records a warning under code; the first time, it is also logged.
func (p *Prober) Warn(code, message string) {
	p.mu.Lock()
	_, seen := p.warnings[code]
	p.warnings[code] = Warning{code, message}
	p.mu.Unlock()
	if !seen {
		p.log.Warn(message, "code", code)
	}
}

// Clear removes a warning.
func (p *Prober) Clear(code string) {
	p.mu.Lock()
	delete(p.warnings, code)
	p.mu.Unlock()
}

// Unsafe returns why the sidecar refuses to serve, or "".
func (p *Prober) Unsafe() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status.Unsafe
}

// Status returns the latest status with the current warnings, sorted.
func (p *Prober) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := p.status
	st.Warnings = make([]Warning, 0, len(p.warnings))
	for _, w := range p.warnings {
		st.Warnings = append(st.Warnings, w)
	}
	sort.Slice(st.Warnings, func(i, j int) bool { return st.Warnings[i].Code < st.Warnings[j].Code })
	return st
}
