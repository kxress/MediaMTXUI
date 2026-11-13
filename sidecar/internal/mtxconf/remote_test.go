package mtxconf

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mtxui/internal/store"
)

type fakeRemoteConfig struct {
	mu      sync.Mutex
	content []byte
	offline bool
}

type failRemoteVerifier struct{}

func (failRemoteVerifier) Get(_ context.Context, path string) (int, []byte, error) {
	if path == "/v3/info" {
		return http.StatusOK, []byte(`{"version":"v1.21.3"}`), nil
	}
	return http.StatusBadGateway, nil, nil
}

func (f *fakeRemoteConfig) Read(context.Context) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return nil, "", errors.New("agent offline")
	}
	b := append([]byte(nil), f.content...)
	return b, sha(b), nil
}

func (f *fakeRemoteConfig) Validate(_ context.Context, content []byte) error {
	if len(content) == 0 {
		return &InvalidError{Err: errors.New("empty remote config")}
	}
	return nil
}

func (f *fakeRemoteConfig) Apply(_ context.Context, content []byte, expectSHA string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return errors.New("agent offline")
	}
	if sha(f.content) != expectSHA {
		return ErrConflict
	}
	f.content = append([]byte(nil), content...)
	return nil
}

func TestRemoteWriterReconcilesWritesAndDetectsDrift(t *testing.T) {
	ctx := context.Background()
	good := seed(t, params)
	remote := &fakeRemoteConfig{content: append([]byte(nil), good...)}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cache := filepath.Join(t.TempDir(), "config", "mediamtx.yml")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &Writer{
		Path: cache, LockPath: filepath.Join(filepath.Dir(cache), "config.lock"),
		Rules: Rules{AuthURL: authURL}, Checker: fakeChecker{}, Store: st, Remote: remote,
	}
	if out, err := w.Reconcile(ctx, good); err != nil || out.Message != "adopted the remote MediaMTX config" {
		t.Fatalf("initial reconcile = %#v, %v", out, err)
	}
	before, sum, err := w.Current()
	if err != nil || sum != sha(good) {
		t.Fatalf("current = %q %v", sum, err)
	}
	after := append(append([]byte(nil), before...), []byte("# remote edit\n")...)
	res, err := w.Replace(ctx, after, sum, "admin", "remote edit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != sha(after) {
		t.Fatalf("write hash = %q, want %q", res.SHA256, sha(after))
	}
	if got, err := os.ReadFile(cache); err != nil || string(got) != string(after) {
		t.Fatalf("local mirror = %q, %v", got, err)
	}
	remote.mu.Lock()
	remote.content = append(append([]byte(nil), after...), []byte("# changed externally\n")...)
	remote.mu.Unlock()
	if out, err := w.Reconcile(ctx, good); err != nil || !out.Notable || out.Message == "" {
		t.Fatalf("external drift reconcile = %#v, %v", out, err)
	}
}

func TestRemoteWriterUsesCachedConfigDuringOutageAndFailsWritesClosed(t *testing.T) {
	ctx := context.Background()
	good := seed(t, params)
	remote := &fakeRemoteConfig{content: append([]byte(nil), good...)}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cache := filepath.Join(t.TempDir(), "config", "mediamtx.yml")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &Writer{
		Path: cache, LockPath: filepath.Join(filepath.Dir(cache), "config.lock"),
		Rules: Rules{AuthURL: authURL}, Checker: fakeChecker{}, Store: st, Remote: remote,
	}
	if _, err := w.Reconcile(ctx, good); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	remote.offline = true
	remote.mu.Unlock()
	if out, err := w.Reconcile(ctx, good); err != nil || !out.Notable {
		t.Fatalf("offline reconcile = %#v, %v", out, err)
	}
	if got, sum, err := w.Current(); err != nil || string(got) != string(good) || sum != sha(good) {
		t.Fatalf("cached current = %q %q %v", got, sum, err)
	}
	if _, err := w.Replace(ctx, append(good, '\n'), sha(good), "admin", "offline edit", nil); err == nil {
		t.Fatal("remote config write succeeded while the agent was offline")
	}
}

func TestRemoteWriterRollsBackRemoteConfigWhenMediaMTXStopsAnswering(t *testing.T) {
	ctx := context.Background()
	good := seed(t, params)
	remote := &fakeRemoteConfig{content: append([]byte(nil), good...)}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cache := filepath.Join(t.TempDir(), "config", "mediamtx.yml")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &Writer{
		Path: cache, LockPath: filepath.Join(filepath.Dir(cache), "config.lock"),
		Rules: Rules{AuthURL: authURL}, Checker: fakeChecker{}, Store: st, Remote: remote,
		Verifier: &Verifier{API: failRemoteVerifier{}, Down: time.Millisecond, Timeout: 10 * time.Millisecond, Interval: time.Millisecond},
	}
	if _, err := w.Reconcile(ctx, good); err != nil {
		t.Fatal(err)
	}
	after := bytes.Replace(good, []byte("logLevel: info"), []byte("logLevel: debug"), 1)
	if bytes.Equal(after, good) {
		t.Fatal("test config has no logLevel setting")
	}
	if _, err := w.Replace(ctx, after, sha(good), "admin", "test rollback", nil); err == nil {
		t.Fatal("write unexpectedly verified after MediaMTX stopped answering")
	}
	remote.mu.Lock()
	got := append([]byte(nil), remote.content...)
	remote.mu.Unlock()
	if !bytes.Equal(got, good) {
		t.Fatalf("remote config was not restored: sha=%s want %s", sha(got), sha(good))
	}
	if got, err := os.ReadFile(cache); err != nil || !bytes.Equal(got, good) {
		t.Fatalf("local config mirror was not restored: %v", err)
	}
}
