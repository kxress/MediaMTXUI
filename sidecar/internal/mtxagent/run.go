package mtxagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mtxui/internal/buildinfo"
	"mtxui/internal/mtxconf"
)

// Config contains the agent's fixed, operator-supplied endpoints and trust material.
type Config struct {
	Listen            string
	AuthListen        string
	APIRoot           string
	ServerCert        string
	ServerKey         string
	ClientCA          string
	ClientFingerprint []byte
	AuthURL           string
	AgentID           string
	AuthKey           []byte
	ConfigPath        string
	MediaMTXBin       string
	RecordingsDir     string
}

// LoadConfig reads the optional agent service configuration from environment variables.
func LoadConfig(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Listen:        get("MTXUI_AGENT_LISTEN", "127.0.0.1:9443"),
		AuthListen:    get("MTXUI_AGENT_AUTH_LISTEN", "127.0.0.1:9081"),
		APIRoot:       get("MTXUI_AGENT_API_URL", "http://127.0.0.1:9997"),
		ServerCert:    strings.TrimSpace(getenv("MTXUI_AGENT_TLS_CERT")),
		ServerKey:     strings.TrimSpace(getenv("MTXUI_AGENT_TLS_KEY")),
		ClientCA:      strings.TrimSpace(getenv("MTXUI_AGENT_CLIENT_CA")),
		AuthURL:       strings.TrimSpace(getenv("MTXUI_AGENT_AUTH_URL")),
		AgentID:       strings.TrimSpace(getenv("MTXUI_AGENT_ID")),
		ConfigPath:    strings.TrimSpace(getenv("MTXUI_AGENT_CONFIG_PATH")),
		MediaMTXBin:   strings.TrimSpace(getenv("MTXUI_AGENT_MEDIAMTX_BIN")),
		RecordingsDir: strings.TrimSpace(getenv("MTXUI_AGENT_RECORDINGS_DIR")),
	}
	var err error
	if c.ClientFingerprint, err = decodeHexSetting(getenv("MTXUI_AGENT_CLIENT_CERT_SHA256"), "MTXUI_AGENT_CLIENT_CERT_SHA256", 32); err != nil {
		return Config{}, err
	}
	if c.AuthKey, err = decodeHexSetting(getenv("MTXUI_AGENT_AUTH_KEY"), "MTXUI_AGENT_AUTH_KEY", 32); err != nil {
		return Config{}, err
	}
	for name, value := range map[string]string{
		"MTXUI_AGENT_TLS_CERT": c.ServerCert, "MTXUI_AGENT_TLS_KEY": c.ServerKey, "MTXUI_AGENT_CLIENT_CA": c.ClientCA,
		"MTXUI_AGENT_AUTH_URL": c.AuthURL, "MTXUI_AGENT_ID": c.AgentID, "MTXUI_AGENT_CONFIG_PATH": c.ConfigPath,
		"MTXUI_AGENT_MEDIAMTX_BIN": c.MediaMTXBin, "MTXUI_AGENT_RECORDINGS_DIR": c.RecordingsDir,
	} {
		if value == "" {
			return Config{}, fmt.Errorf("%s is required for agent mode", name)
		}
	}
	if err := validateListen(c.Listen, false); err != nil {
		return Config{}, fmt.Errorf("MTXUI_AGENT_LISTEN: %w", err)
	}
	if err := validateListen(c.AuthListen, true); err != nil {
		return Config{}, fmt.Errorf("MTXUI_AGENT_AUTH_LISTEN: %w", err)
	}
	if err := validateAPIRoot(c.APIRoot); err != nil {
		return Config{}, fmt.Errorf("MTXUI_AGENT_API_URL: %w", err)
	}
	for key, value := range map[string]string{"MTXUI_AGENT_CONFIG_PATH": c.ConfigPath, "MTXUI_AGENT_MEDIAMTX_BIN": c.MediaMTXBin, "MTXUI_AGENT_RECORDINGS_DIR": c.RecordingsDir} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return Config{}, fmt.Errorf("%s must be an absolute clean path", key)
		}
	}
	if _, err := NewAuthRelay(c.AuthURL, c.AgentID, c.AuthKey, nil); err != nil {
		return Config{}, fmt.Errorf("agent auth relay: %w", err)
	}
	return c, nil
}

func decodeHexSetting(value, name string, size int) ([]byte, error) {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("%s must be a %d-character hexadecimal value", name, size*2)
	}
	return decoded, nil
}

func validateListen(addr string, loopback bool) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("must be an IP:port address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("host must be an IP address")
	}
	if loopback && !ip.IsLoopback() {
		return errors.New("must bind to a loopback address")
	}
	return nil
}

func validateAPIRoot(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be an HTTP(S) origin without credentials, path, query or fragment")
	}
	host := strings.Trim(u.Hostname(), "[]")
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("must point to a loopback MediaMTX Control API")
		}
	}
	return nil
}

// Run serves the mTLS management endpoint and a separate loopback-only MediaMTX auth callback.
func Run(ctx context.Context, c Config, log *slog.Logger) error {
	serverCert, err := tls.LoadX509KeyPair(c.ServerCert, c.ServerKey)
	if err != nil {
		return fmt.Errorf("load agent server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return fmt.Errorf("read agent client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return errors.New("agent client CA file contains no certificates")
	}
	api, err := NewAPI(c.APIRoot, nil)
	if err != nil {
		return err
	}
	config := &ConfigService{
		Path: c.ConfigPath, Validator: mtxconf.Validator{Bin: c.MediaMTXBin},
		Rules: mtxconf.Rules{AuthURL: "http://" + c.AuthListen + "/internal/auth", RecordingsDir: c.RecordingsDir, PrivateControl: true},
	}
	relay, err := NewAuthRelay(c.AuthURL, c.AgentID, c.AuthKey, nil)
	if err != nil {
		return err
	}
	mgmt := http.NewServeMux()
	mgmt.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q,"capabilities":["control-api","auth-relay","config-transactions"]}`, buildinfo.Version)
	})
	mgmt.Handle(apiPrefix+"/", api)
	mgmt.Handle("/v1/config", config)
	mgmt.Handle("/v1/config/", config)
	callback := http.NewServeMux()
	callback.Handle("/internal/auth", relay)
	mgmtServer := &http.Server{
		Addr: c.Listen, Handler: mgmt, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}, ClientCAs: clientCAs,
			ClientAuth: tls.RequireAndVerifyClientCert,
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("agent client certificate missing")
				}
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(sum[:], c.ClientFingerprint) != 1 {
					return errors.New("agent client certificate is not the configured management identity")
				}
				return nil
			},
		},
	}
	authServer := &http.Server{Addr: c.AuthListen, Handler: callback, ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	var listenConfig net.ListenConfig
	mgmtLn, err := listenConfig.Listen(ctx, "tcp", c.Listen)
	if err != nil {
		return err
	}
	authLn, err := listenConfig.Listen(ctx, "tcp", c.AuthListen)
	if err != nil {
		_ = mgmtLn.Close()
		return err
	}
	errC := make(chan error, 2)
	go func() { errC <- mgmtServer.ServeTLS(mgmtLn, "", "") }()
	go func() { errC <- authServer.Serve(authLn) }()
	log.Info("remote MediaMTX agent started", "listen", c.Listen, "auth_listen", c.AuthListen, "agent_id", c.AgentID)
	select {
	case <-ctx.Done():
	case err := <-errC:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = mgmtServer.Close()
			_ = authServer.Close()
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgmtServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("agent management server shutdown", "err", err)
	}
	if err := authServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("agent auth relay shutdown", "err", err)
	}
	return nil
}
