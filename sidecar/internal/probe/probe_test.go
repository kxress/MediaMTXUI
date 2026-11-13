package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"mtxui/internal/mtxtest"
)

type principal struct{}

func (principal) Authorize(r *http.Request) { r.SetBasicAuth("mtxui-sidecar", "s") }

// fakeMTX answers /v3/paths/list per anonymous and /v3/info with a version.
func fakeMTX(anonymousCode int, version string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, authed := r.BasicAuth()
		switch {
		case r.URL.Path == "/v3/paths/list" && !authed:
			w.WriteHeader(anonymousCode)
		case r.URL.Path == "/v3/info" && authed:
			_, _ = io.WriteString(w, `{"version":"`+version+`"}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSafeMediaMTX(t *testing.T) {
	srv := fakeMTX(http.StatusUnauthorized, "v1.21.1")
	defer srv.Close()
	p := New(srv.URL, principal{}, "1.21.1", "mtx.example.com", quiet())
	p.Check(context.Background())
	st := p.Status()
	if !st.Reachable || st.APIProtected == nil || !*st.APIProtected || st.Unsafe != "" || p.Unsafe() != "" {
		t.Fatalf("status %+v", st)
	}
	if st.Version != "1.21.1" || len(st.Warnings) != 0 {
		t.Errorf("version %q warnings %v", st.Version, st.Warnings)
	}
}

func TestAnonymousAPIIsUnsafe(t *testing.T) {
	srv := fakeMTX(http.StatusOK, "v1.21.1")
	defer srv.Close()
	p := New(srv.URL, principal{}, "1.21.1", "mtx.example.com", quiet())
	p.Check(context.Background())
	if p.Unsafe() == "" || *p.Status().APIProtected {
		t.Fatalf("an anonymous API was not flagged: %+v", p.Status())
	}
}

func TestUnreachableIsUnknownNotUnsafe(t *testing.T) {
	p := New("http://127.0.0.1:1", principal{}, "1.21.1", "mtx.example.com", quiet())
	p.Check(context.Background())
	st := p.Status()
	if st.Reachable || st.APIProtected != nil || st.Unsafe != "" || st.Failure != "media_api_unavailable" {
		t.Fatalf("status %+v", st)
	}
}

func TestRemoteManagementAndMediaFailuresAreDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok","version":"test","capabilities":["control-api"]}`)
		case "/v1/media/v3/paths/list":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p := New(server.URL+"/v1/media", principal{}, "1.21.1", "media.example.test", quiet())
	p.SetHTTPClient(server.Client())
	p.SetManagementHealthURL(server.URL + "/v1/health")
	p.Check(context.Background())
	st := p.Status()
	if st.ManagementReachable == nil || !*st.ManagementReachable || st.ManagementVersion != "test" || st.Failure != "media_api_unavailable" || st.Reachable {
		t.Fatalf("agent up / MediaMTX API down = %+v", st)
	}
	server.Close()
	p.Check(context.Background())
	st = p.Status()
	if st.ManagementReachable == nil || *st.ManagementReachable || st.Failure != "management_unavailable" || st.Reachable {
		t.Fatalf("agent down = %+v", st)
	}
}

func TestRemoteProbeChecksMediaMTXAuthorizationBehindAuthenticatedAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok","version":"test","capabilities":["control-api"]}`)
		case "/v1/media/v3/paths/list":
			if _, _, ok := r.BasicAuth(); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/media/v3/info":
			if _, _, ok := r.BasicAuth(); ok {
				_, _ = io.WriteString(w, `{"version":"v1.21.3"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	p := New(server.URL+"/v1/media", principal{}, "1.21.3", "media.example.test", quiet())
	p.SetHTTPClient(server.Client())
	p.SetManagementHealthURL(server.URL + "/v1/health")
	p.Check(context.Background())
	st := p.Status()
	if st.ManagementReachable == nil || !*st.ManagementReachable || !st.Reachable || st.APIProtected == nil || !*st.APIProtected || st.Version != "1.21.3" {
		t.Fatalf("remote probe = %+v", st)
	}
}

func TestVersionMismatchWarns(t *testing.T) {
	srv := fakeMTX(http.StatusUnauthorized, "v1.22.0")
	defer srv.Close()
	p := New(srv.URL, principal{}, "1.21.1", "mtx.example.com", quiet())
	p.Check(context.Background())
	st := p.Status()
	if len(st.Warnings) != 1 || st.Warnings[0].Code != "version" || !strings.Contains(st.Warnings[0].Message, "1.22.0") {
		t.Fatalf("warnings %v", st.Warnings)
	}
}

func TestExposure(t *testing.T) {
	p := New("http://127.0.0.1:1", principal{}, "1.21.1", "mtx.example.com", quiet())
	p.StackSubnet = netip.MustParsePrefix("172.29.44.0/24")
	p.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
	}
	open := map[string]bool{"mtx.example.com:9997": true}
	p.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		if open[addr] {
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		}
		return nil, errors.New("refused")
	}
	p.CheckExposure(context.Background())
	st := p.Status()
	if len(st.Warnings) != 1 || st.Warnings[0].Code != "exposed_9997" || !strings.Contains(st.Warnings[0].Message, "Control API") {
		t.Fatalf("warnings %v", st.Warnings)
	}
	delete(open, "mtx.example.com:9997")
	p.CheckExposure(context.Background())
	if len(p.Status().Warnings) != 0 {
		t.Errorf("a closed port still warns: %v", p.Status().Warnings)
	}

	// On the stack's own network (or loopback) every port answers, published or not: nothing to warn about.
	for _, host := range []string{"mediamtx", "127.0.0.1", "::1"} {
		q := New("http://127.0.0.1:1", principal{}, "1.21.1", host, quiet())
		q.StackSubnet = netip.MustParsePrefix("172.29.44.0/24")
		q.lookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("172.29.44.3")}, nil
		}
		q.dial = func(context.Context, string, string) (net.Conn, error) {
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		}
		q.CheckExposure(context.Background())
		if w := q.Status().Warnings; len(w) != 0 {
			t.Errorf("%s: warnings %v", host, w)
		}
	}
}

func TestRunDefersExposureUntilMediaMTXAnswers(t *testing.T) {
	p := New("http://127.0.0.1:1", principal{}, "1.21.1", "mtx.example.com", quiet())
	p.StackSubnet = netip.MustParsePrefix("172.29.44.0/24")
	p.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
	}
	dials := 0
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx)
	if dials != 0 {
		t.Fatalf("checked exposure %d times before MediaMTX answered", dials)
	}
}

func TestRunChecksExposureAfterMediaMTXAnswers(t *testing.T) {
	srv := fakeMTX(http.StatusUnauthorized, "1.21.1")
	defer srv.Close()
	p := New(srv.URL, principal{}, "1.21.1", "mtx.example.com", quiet())
	p.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	dials := 0
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			cancel()
		}
		return nil, errors.New("refused")
	}
	p.Run(ctx)
	if !p.Status().Reachable || dials != len(neverPublished) {
		t.Fatalf("reachable=%v exposure checks=%d", p.Status().Reachable, dials)
	}
}

func TestWarnAndClear(t *testing.T) {
	p := New("http://127.0.0.1:1", principal{}, "1.21.1", "h", quiet())
	p.Warn("b", "second")
	p.Warn("a", "first")
	p.Warn("a", "first again")
	st := p.Status()
	if len(st.Warnings) != 2 || st.Warnings[0].Code != "a" || st.Warnings[0].Message != "first again" {
		t.Fatalf("warnings %v", st.Warnings)
	}
	p.Clear("a")
	if len(p.Status().Warnings) != 1 {
		t.Errorf("Clear did not remove the warning: %v", p.Status().Warnings)
	}
}

// The safety check against the real MediaMTX: a config that lets anyone use the API is flagged, one that sends
// every request to an authentication endpoint is not.
func TestSafetyCheckWithMediaMTX(t *testing.T) {
	mtxtest.Bin(t)
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer deny.Close()
	for _, tt := range []struct {
		name, auth string
		unsafe     bool
	}{
		{"anonymous API access", "authMethod: internal\nauthInternalUsers:\n  - user: any\n    permissions:\n      - action: api\n", true},
		{"HTTP authentication", "authMethod: http\nauthHTTPAddress: " + deny.URL + "/internal/auth\nauthHTTPExclude: []\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := mtxtest.FreePort(t)
			mtxtest.Start(t, fmt.Sprintf("%sapi: yes\napiAddress: 127.0.0.1:%d\nrtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\n", tt.auth, api), api)
			p := New(fmt.Sprintf("http://127.0.0.1:%d", api), principal{}, "1.21.1", "127.0.0.1", quiet())
			p.Check(context.Background())
			if got := p.Unsafe() != ""; got != tt.unsafe {
				t.Fatalf("unsafe = %v (%q), want %v", got, p.Unsafe(), tt.unsafe)
			}
		})
	}
}
