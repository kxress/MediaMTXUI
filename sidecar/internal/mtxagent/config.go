package mtxagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mtxui/internal/mtxconf"
)

const (
	maxConfig     = 1 << 20
	maxConfigJSON = 6*maxConfig + 4096 // worst-case JSON escaping for every byte in the YAML string
)

type configDocument struct {
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

type configWrite struct {
	Content   string `json:"content"`
	ExpectSHA string `json:"expectSHA"`
}

// ConfigValidator validates candidate configuration with the MediaMTX version installed on the media host.
type ConfigValidator interface {
	Validate(ctx context.Context, content []byte) error
}

// ConfigService exposes only the one operator-configured MediaMTX YAML file.
type ConfigService struct {
	Path      string
	Validator ConfigValidator
	Rules     mtxconf.Rules
	mu        sync.Mutex
}

func (s *ConfigService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/config" && r.Method == http.MethodGet:
		s.read(w)
	case r.URL.Path == "/v1/config/validate" && r.Method == http.MethodPost:
		s.validate(w, r)
	case r.URL.Path == "/v1/config" && r.Method == http.MethodPut:
		s.apply(w, r)
	case (r.URL.Path == "/v1/config" || r.URL.Path == "/v1/config/validate"):
		w.Header().Set("Allow", "GET, PUT, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

func (s *ConfigService) read(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.readFile()
	if err != nil {
		http.Error(w, "MediaMTX configuration is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(configDocument{Content: string(b), SHA256: hash(b)})
}

func (s *ConfigService) validate(w http.ResponseWriter, r *http.Request) {
	var req configDocument
	if !decodeConfig(w, r, &req) {
		return
	}
	if err := s.check(r.Context(), []byte(req.Content)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"valid":true}`)
}

func (s *ConfigService) apply(w http.ResponseWriter, r *http.Request) {
	var req configWrite
	if !decodeConfig(w, r, &req) {
		return
	}
	content := []byte(req.Content)
	if len(req.ExpectSHA) != sha256.Size*2 {
		http.Error(w, "expectSHA must be the current SHA-256", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := s.readFile()
	if err != nil {
		http.Error(w, "MediaMTX configuration is unavailable", http.StatusServiceUnavailable)
		return
	}
	if hash(before) != req.ExpectSHA {
		http.Error(w, "MediaMTX configuration changed", http.StatusConflict)
		return
	}
	if err := s.check(r.Context(), content); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Validation can take long enough for an operator to edit the file outside the agent. Recheck the revision just
	// before the atomic rename so the compare-and-swap also protects against those edits.
	current, err := s.readFile()
	if err != nil {
		http.Error(w, "MediaMTX configuration is unavailable", http.StatusServiceUnavailable)
		return
	}
	if hash(current) != req.ExpectSHA {
		http.Error(w, "MediaMTX configuration changed", http.StatusConflict)
		return
	}
	if err := mtxconf.AtomicWrite(s.Path, content); err != nil {
		http.Error(w, "cannot atomically update MediaMTX configuration", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(configDocument{SHA256: hash(content)})
}

func (s *ConfigService) readFile() ([]byte, error) {
	if !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path {
		return nil, errors.New("agent config path must be an absolute clean path")
	}
	info, err := os.Lstat(s.Path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("agent config path is not a regular file")
	}
	if info.Size() > maxConfig {
		return nil, errors.New("agent config file exceeds the size limit")
	}
	return os.ReadFile(s.Path)
}

func (s *ConfigService) check(ctx context.Context, content []byte) error {
	if err := s.Rules.Check(content); err != nil {
		return err
	}
	return s.Validator.Validate(ctx, content)
}

func decodeConfig(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength > maxConfigJSON {
		http.Error(w, "configuration is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigJSON)).Decode(dst); err != nil {
		http.Error(w, "invalid configuration request", http.StatusBadRequest)
		return false
	}
	return true
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RemoteConfigClient is the central application's fixed-config protocol adapter.
type RemoteConfigClient struct {
	Base   string
	Client *http.Client
}

var _ mtxconf.RemoteConfig = (*RemoteConfigClient)(nil)

// NewRemoteConfigClient creates an adapter for one HTTPS agent origin using the caller's authenticated HTTP client.
func NewRemoteConfigClient(base string, client *http.Client) (*RemoteConfigClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("remote agent config URL must be an HTTPS origin")
	}
	if client == nil {
		return nil, errors.New("remote agent config client is required")
	}
	return &RemoteConfigClient{Base: strings.TrimSuffix(base, "/"), Client: client}, nil
}

func (c *RemoteConfigClient) Read(ctx context.Context) ([]byte, string, error) {
	var doc configDocument
	if err := c.doJSON(ctx, http.MethodGet, "/v1/config", nil, http.StatusOK, &doc); err != nil {
		return nil, "", err
	}
	content := []byte(doc.Content)
	if len(content) > maxConfig || hash(content) != doc.SHA256 {
		return nil, "", errors.New("remote agent returned an invalid config digest")
	}
	return content, doc.SHA256, nil
}

// Validate asks the agent to validate candidate YAML without applying it.
func (c *RemoteConfigClient) Validate(ctx context.Context, content []byte) error {
	if len(content) > maxConfig {
		return errors.New("configuration is too large")
	}
	var result struct {
		Valid bool `json:"valid"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/config/validate", configDocument{Content: string(content)}, http.StatusOK, &result); err != nil {
		return err
	}
	if !result.Valid {
		return errors.New("remote MediaMTX rejected the configuration")
	}
	return nil
}

// Apply commits candidate YAML only when the agent's current digest matches expectSHA.
func (c *RemoteConfigClient) Apply(ctx context.Context, content []byte, expectSHA string) error {
	if len(content) > maxConfig {
		return errors.New("configuration is too large")
	}
	req := configWrite{Content: string(content), ExpectSHA: expectSHA}
	var result configDocument
	err := c.doJSON(ctx, http.MethodPut, "/v1/config", req, http.StatusOK, &result)
	if err == nil && result.SHA256 == hash(content) {
		return nil
	}
	// A lost response after an atomic commit is ambiguous. Read back the fixed file and accept only the exact content
	// we attempted to apply; otherwise return the original transport or protocol error without retrying a write.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, sum, readErr := c.Read(checkCtx)
	if readErr == nil && sum == hash(content) && string(current) == string(content) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("remote agent did not confirm the configuration update")
}

func (c *RemoteConfigClient) doJSON(ctx context.Context, method, path string, payload any, wantStatus int, dst any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusConflict {
			return mtxconf.ErrConflict
		}
		return fmt.Errorf("remote agent answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if dst == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxConfigJSON)).Decode(dst); err != nil {
		return fmt.Errorf("invalid response from remote agent: %w", err)
	}
	return nil
}
