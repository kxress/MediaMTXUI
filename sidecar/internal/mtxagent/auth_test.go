package mtxagent

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func signedRequest(t *testing.T, key []byte, id string, now time.Time, body []byte) *http.Request {
	t.Helper()
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		t.Fatal(err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	stamp := strconv.FormatInt(now.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "https://ui.example/agent/v1/auth", bytes.NewReader(body))
	req.Header.Set(AgentIDHeader, id)
	req.Header.Set(TimestampHeader, stamp)
	req.Header.Set(NonceHeader, nonce)
	req.Header.Set(SignatureHeader, signature(key, id, stamp, nonce, http.MethodPost, req.URL.EscapedPath(), body))
	return req
}

func TestVerifierAuthenticatesAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{0x5a}, 32)
	v := &Verifier{AgentID: "media-east", Key: key, Now: func() time.Time { return now }}
	body := []byte(`{"ip":"192.0.2.4","action":"publish"}`)
	req := signedRequest(t, key, "media-east", now, body)
	if !v.Verify(req, body) {
		t.Fatal("valid callback signature refused")
	}
	if v.Verify(req, body) {
		t.Fatal("replayed callback accepted")
	}

	other := signedRequest(t, key, "media-east", now, body)
	if v.Verify(other, append(body, ' ')) {
		t.Fatal("body modification accepted")
	}
	other = signedRequest(t, key, "wrong-agent", now, body)
	if v.Verify(other, body) {
		t.Fatal("wrong agent accepted")
	}
	other = signedRequest(t, key, "media-east", now.Add(-time.Minute), body)
	if v.Verify(other, body) {
		t.Fatal("stale callback accepted")
	}
	other = signedRequest(t, key, "media-east", now, body)
	other.URL.Path = "/agent/v1/other"
	if v.Verify(other, body) {
		t.Fatal("signature accepted on a different path")
	}
}

func TestNewAuthRelayRequiresHTTPSAndExactCallbackPath(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	for _, u := range []string{
		"http://ui.example/agent/v1/auth",
		"https://ui.example/agent/v1/auth?x=1",
		"https://ui.example/agent/v1/other",
		"https://user:pass@ui.example/agent/v1/auth",
	} {
		if _, err := NewAuthRelay(u, "media-east", key, nil); err == nil {
			t.Errorf("NewAuthRelay(%q) succeeded", u)
		}
	}
	if _, err := NewAuthRelay("https://ui.example/agent/v1/auth", "media-east", key, nil); err != nil {
		t.Fatalf("valid relay URL: %v", err)
	}
}
