package mtxagent

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// AgentIDHeader identifies the configured agent that signed a callback.
	AgentIDHeader = "X-MTXUI-Agent-ID"
	// TimestampHeader carries the callback signature's creation time.
	TimestampHeader = "X-MTXUI-Timestamp"
	// NonceHeader carries a one-time value that prevents callback replay.
	NonceHeader = "X-MTXUI-Nonce"
	// SignatureHeader carries the callback HMAC.
	SignatureHeader   = "X-MTXUI-Signature"
	maxAuthBody       = 64 << 10
	maxClockSkew      = 30 * time.Second
	maxRememberedAuth = 100_000
)

// AuthRelay accepts only local MediaMTX callbacks and forwards them over HTTPS with a per-agent HMAC.
// The shared key authenticates the management agent, not the stream credential in the callback body.
type AuthRelay struct {
	central *url.URL
	agentID string
	key     []byte
	client  *http.Client
	now     func() time.Time
}

// NewAuthRelay builds an auth callback relay. It requires HTTPS so callback passwords and tokens are encrypted in transit.
func NewAuthRelay(centralURL, agentID string, key []byte, client *http.Client) (*AuthRelay, error) {
	u, err := url.Parse(centralURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.EscapedPath() != "/agent/v1/auth" {
		return nil, errors.New("agent authentication callback URL must be HTTPS at /agent/v1/auth without credentials or a query")
	}
	if agentID == "" || len(agentID) > 128 || strings.ContainsAny(agentID, "\r\n\x00") {
		return nil, errors.New("agent ID is missing or invalid")
	}
	if len(key) < 32 {
		return nil, errors.New("agent authentication key must be at least 32 bytes")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &AuthRelay{central: u, agentID: agentID, key: append([]byte(nil), key...), client: client, now: time.Now}, nil
}

func (a *AuthRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	peer := net.ParseIP(host)
	if err != nil || peer == nil || !peer.IsLoopback() {
		http.Error(w, "local callback only", http.StatusForbidden)
		return
	}
	if r.ContentLength > maxAuthBody {
		http.Error(w, "callback too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuthBody+1))
	if err != nil || len(body) > maxAuthBody {
		http.Error(w, "callback too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.central.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "callback unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := SignRequest(req, a.agentID, a.key, a.now()); err != nil {
		http.Error(w, "callback unavailable", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		http.Error(w, "management authorization service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		http.Error(w, "management authorization service unavailable", http.StatusServiceUnavailable)
		return
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxAuthBody))
}

// SignRequest adds the callback relay's agent identity, short-lived timestamp, unique nonce and HMAC signature.
func SignRequest(req *http.Request, agentID string, key []byte, now time.Time) error {
	if req.Method != http.MethodPost || req.URL == nil || req.URL.EscapedPath() != "/agent/v1/auth" || req.URL.RawQuery != "" || agentID == "" || len(key) < 32 {
		return errors.New("request is not a valid authenticated callback")
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	stamp := strconv.FormatInt(now.Unix(), 10)
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	body, err := io.ReadAll(io.LimitReader(req.Body, maxAuthBody+1))
	if err != nil || len(body) > maxAuthBody {
		return errors.New("callback body exceeds size limit")
	}
	if req.Body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	req.Header.Set(AgentIDHeader, agentID)
	req.Header.Set(TimestampHeader, stamp)
	req.Header.Set(NonceHeader, nonce)
	req.Header.Set(SignatureHeader, signature(key, agentID, stamp, nonce, req.Method, req.URL.EscapedPath(), body))
	return nil
}

// Verifier authenticates remote callback relays and rejects stale or replayed requests.
type Verifier struct {
	AgentID string
	Key     []byte
	Now     func() time.Time

	mu       sync.Mutex
	seen     map[string]time.Time
	lastTrim time.Time
}

// Verify checks an already bounded request body against the signature headers and records the nonce once.
func (v *Verifier) Verify(r *http.Request, body []byte) bool {
	if r.Method != http.MethodPost || len(body) > maxAuthBody || len(v.Key) < 32 || v.AgentID == "" || r.URL.EscapedPath() != "/agent/v1/auth" || r.URL.RawQuery != "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(AgentIDHeader)), []byte(v.AgentID)) != 1 {
		return false
	}
	stampText := r.Header.Get(TimestampHeader)
	stamp, err := strconv.ParseInt(stampText, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	when := time.Unix(stamp, 0)
	if now.Sub(when) > maxClockSkew || when.Sub(now) > maxClockSkew {
		return false
	}
	nonce := r.Header.Get(NonceHeader)
	decoded, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(decoded) != 24 {
		return false
	}
	provided, err := hex.DecodeString(r.Header.Get(SignatureHeader))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	want, _ := hex.DecodeString(signature(v.Key, v.AgentID, stampText, nonce, r.Method, r.URL.EscapedPath(), body))
	if subtle.ConstantTimeCompare(provided, want) != 1 {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = make(map[string]time.Time)
	}
	if now.Sub(v.lastTrim) > maxClockSkew || len(v.seen) >= maxRememberedAuth {
		for k, expires := range v.seen {
			if !now.Before(expires) {
				delete(v.seen, k)
			}
		}
		v.lastTrim = now
	}
	key := v.AgentID + "\x00" + nonce
	if _, exists := v.seen[key]; exists || len(v.seen) >= maxRememberedAuth {
		return false
	}
	v.seen[key] = when.Add(maxClockSkew)
	return true
}

func signature(key []byte, id, stamp, nonce, method, path string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	message := method + "\n" + path + "\n" + id + "\n" + stamp + "\n" + nonce + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, message)
	return hex.EncodeToString(mac.Sum(nil))
}
