package mtxagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mtxui/internal/mtxconf"
)

type acceptConfig struct{}

func (acceptConfig) Validate(context.Context, []byte) error { return nil }

type editDuringValidation struct {
	path    string
	content []byte
}

func (v editDuringValidation) Validate(context.Context, []byte) error {
	return os.WriteFile(v.path, v.content, 0o640)
}

func TestConfigServiceCASAndPrivateControlListeners(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mediamtx.yml")
	seed, err := mtxconf.Seed(mtxconf.SeedParams{
		MediaMTXVersion: "1.21.3", AuthURL: "http://127.0.0.1:9081/internal/auth",
		PublicHost: "media.example.test", ControlHost: "127.0.0.1", MediaHost: "0.0.0.0",
		RecordingsDir: "/srv/media/recordings",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0o640); err != nil {
		t.Fatal(err)
	}
	svc := &ConfigService{
		Path: path, Validator: acceptConfig{}, Rules: mtxconf.Rules{AuthURL: "http://127.0.0.1:9081/internal/auth", RecordingsDir: "/srv/media/recordings", PrivateControl: true},
	}

	get := httptest.NewRecorder()
	svc.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", get.Code, get.Body.String())
	}
	var doc configDocument
	if err := json.Unmarshal(get.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SHA256 != hash([]byte(doc.Content)) {
		t.Fatal("GET returned a bad digest")
	}

	updated := doc.Content + "# remote update\n"
	writeBody, _ := json.Marshal(configWrite{Content: updated, ExpectSHA: doc.SHA256})
	put := httptest.NewRecorder()
	svc.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(string(writeBody))))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", put.Code, put.Body.String())
	}
	var result configDocument
	if err := json.Unmarshal(put.Body.Bytes(), &result); err != nil || result.SHA256 != hash([]byte(updated)) {
		t.Fatalf("PUT result = %#v, %v", result, err)
	}

	conflictBody, _ := json.Marshal(configWrite{Content: doc.Content, ExpectSHA: doc.SHA256})
	conflict := httptest.NewRecorder()
	svc.ServeHTTP(conflict, httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(string(conflictBody))))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d", conflict.Code)
	}

	unsafe, err := mtxconf.Seed(mtxconf.SeedParams{
		MediaMTXVersion: "1.21.3", AuthURL: "http://127.0.0.1:9081/internal/auth", PublicHost: "media.example.test",
		RecordingsDir: "/srv/media/recordings",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.check(ctx, unsafe); err == nil || !strings.Contains(err.Error(), "apiAddress must bind to a loopback") {
		t.Fatalf("unrestricted Control API accepted: %v", err)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != updated {
		t.Fatalf("config changed after conflict: %v", err)
	}
}

func TestConfigServiceRejectsSymlinkConfigPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yml")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "mediamtx.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	svc := &ConfigService{Path: link}
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("symlink config status = %d", w.Code)
	}
}

func TestRemoteConfigClientValidatesReadsAndUsesCAS(t *testing.T) {
	ctx := context.Background()
	seed, err := mtxconf.Seed(mtxconf.SeedParams{
		MediaMTXVersion: "1.21.3", AuthURL: "http://127.0.0.1:9081/internal/auth",
		PublicHost: "media.example.test", ControlHost: "127.0.0.1", MediaHost: "0.0.0.0",
		RecordingsDir: "/srv/media/recordings",
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "mediamtx.yml")
	if err := os.WriteFile(configPath, seed, 0o640); err != nil {
		t.Fatal(err)
	}
	service := &ConfigService{
		Path: configPath, Validator: acceptConfig{},
		Rules: mtxconf.Rules{AuthURL: "http://127.0.0.1:9081/internal/auth", RecordingsDir: "/srv/media/recordings", PrivateControl: true},
	}
	server := httptest.NewTLSServer(service)
	defer server.Close()
	client, err := NewRemoteConfigClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	content, sum, err := client.Read(ctx)
	if err != nil || string(content) != string(seed) || sum != hash(seed) {
		t.Fatalf("read = sha %q, err %v", sum, err)
	}
	updated := append(append([]byte(nil), content...), []byte("# client protocol test\n")...)
	if err := client.Validate(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(ctx, updated, sum); err != nil {
		t.Fatal(err)
	}
	got, gotSum, err := client.Read(ctx)
	if err != nil || string(got) != string(updated) || gotSum != hash(updated) {
		t.Fatalf("read after apply = sha %q, err %v", gotSum, err)
	}
	if err := client.Apply(ctx, seed, sum); !errors.Is(err, mtxconf.ErrConflict) {
		t.Fatalf("stale compare-and-swap error = %v", err)
	}
}

func TestConfigServiceDetectsExternalEditDuringValidation(t *testing.T) {
	seed, err := mtxconf.Seed(mtxconf.SeedParams{
		MediaMTXVersion: "1.21.3", AuthURL: "http://127.0.0.1:9081/internal/auth",
		PublicHost: "media.example.test", ControlHost: "127.0.0.1", MediaHost: "0.0.0.0",
		RecordingsDir: "/srv/media/recordings",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mediamtx.yml")
	if err := os.WriteFile(path, seed, 0o640); err != nil {
		t.Fatal(err)
	}
	external := append(append([]byte(nil), seed...), []byte("# external operator edit\n")...)
	svc := &ConfigService{
		Path: path, Validator: editDuringValidation{path: path, content: external},
		Rules: mtxconf.Rules{AuthURL: "http://127.0.0.1:9081/internal/auth", RecordingsDir: "/srv/media/recordings", PrivateControl: true},
	}
	body, err := json.Marshal(configWrite{Content: string(seed), ExpectSHA: hash(seed)})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(string(body))))
	if w.Code != http.StatusConflict {
		t.Fatalf("apply status = %d: %s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(external) {
		t.Fatalf("external edit was overwritten: %v", err)
	}
}
