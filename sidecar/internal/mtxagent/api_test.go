package mtxagent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIAllowlistAndAuthForwarding(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		got.Header = r.Header.Clone()
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"version":"v1.21.3"}`)
	}))
	defer upstream.Close()
	api, err := NewAPI(upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://agent/v1/media"+path, nil)
		req.SetBasicAuth("mtxui-sidecar", "opaque-secret")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		return w
	}

	if w := call("/v3/info", http.MethodGet); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1.21.3") {
		t.Fatalf("allowed API response = %d %s", w.Code, w.Body.String())
	}
	if got == nil || got.URL.Path != "/v3/info" || got.Header.Get("Authorization") == "" {
		t.Fatalf("forwarded request = %#v", got)
	}
	if w := call("/v3/auth/jwks/refresh", http.MethodPost); w.Code != http.StatusNotFound {
		t.Fatalf("unused operation status = %d", w.Code)
	}
	if w := call("/v3/info?unexpected=yes", http.MethodGet); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status = %d", w.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "http://agent/v1/media/v3/info", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MediaMTX response = %d", w.Code)
	}
	if got == nil || got.Header.Get("Authorization") != "" {
		t.Fatalf("anonymous Control API probe was not forwarded without credentials: %#v", got)
	}
}

func TestNewAPIRejectsNonOriginURL(t *testing.T) {
	for _, input := range []string{"", "http://user:pass@127.0.0.1:9997", "http://127.0.0.1:9997/v3", "file:///tmp/mtx"} {
		if _, err := NewAPI(input, nil); err == nil {
			t.Errorf("NewAPI(%q) succeeded", input)
		}
	}
}
