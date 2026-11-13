package settings

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func minimal() map[string]string {
	return map[string]string{"MTXUI_PUBLIC_URL": "https://mtx.example.com", "MTXUI_PUBLIC_HOST": "mtx.example.com"}
}

func TestDefaults(t *testing.T) {
	s, err := Load(env(minimal()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Origin() != "https://mtx.example.com" || !s.Secure() {
		t.Errorf("origin %q secure %v", s.Origin(), s.Secure())
	}
	if s.DeploymentMode != "docker" || s.Listen != ":9080" || s.InternalListen != ":9081" || s.DataDir != "/data" || s.MediaMTXHost != "mediamtx" {
		t.Errorf("unexpected defaults: %+v", s)
	}
	if s.AuthURL() != "http://sidecar:9081/internal/auth" {
		t.Errorf("AuthURL = %q", s.AuthURL())
	}
	if s.MediaMTXURL(MediaMTXAPIPort) != "http://mediamtx:9997" {
		t.Errorf("API URL = %q", s.MediaMTXURL(MediaMTXAPIPort))
	}
	if s.ConfigFile() != "/data/config/mediamtx.yml" || s.DBPath() != "/data/state/mtxui.db" {
		t.Errorf("paths: %s %s", s.ConfigFile(), s.DBPath())
	}
	if s.MediaMTXLog() != "/data/logs/mediamtx.log" || s.RecordingDataDir() != "/data/recordings" {
		t.Errorf("Docker local data paths changed: log=%s recordings=%s", s.MediaMTXLog(), s.RecordingDataDir())
	}
	if s.SessionIdleTimeout != 12*time.Hour || s.SessionMaxAge != 168*time.Hour || s.JoinRatePerMinute != 10 || s.LockoutThreshold != 5 {
		t.Errorf("auth defaults: %+v", s)
	}
	if s.StackSubnet.IsValid() || len(s.TrustedProxies) != 0 {
		t.Errorf("optional values should be unset: %+v", s)
	}
	if gb := 3.9; s.HoldingMaxBytes != int64(gb*(1<<30)) || s.HoldingMaxBytes >= 1<<32 {
		t.Errorf("holding clip limit %d: under 4 GiB, which MediaMTX's MP4 reader cannot go past", s.HoldingMaxBytes)
	}
}

func TestNativeLocalSettings(t *testing.T) {
	s, err := Load(env(map[string]string{
		"MTXUI_DEPLOYMENT_MODE":         "native",
		"MTXUI_PUBLIC_URL":              "http://127.0.0.1:9080",
		"MTXUI_PUBLIC_HOST":             "127.0.0.1",
		"MTXUI_LISTEN":                  "127.0.0.1:9080",
		"MTXUI_INTERNAL_LISTEN":         "127.0.0.1:9081",
		"MTXUI_INTERNAL_URL":            "http://127.0.0.1:9081",
		"MTXUI_MEDIAMTX_HOST":           "127.0.0.1",
		"MTXUI_MEDIAMTX_CONTROL_HOST":   "127.0.0.1",
		"MTXUI_DATA_DIR":                "/var/lib/mtxui",
		"MTXUI_HOLDING_DIR":             "/var/lib/mtxui/holding",
		"MTXUI_MEDIAMTX_LOG_PATH":       "/var/lib/mtxui/logs/mediamtx.log",
		"MTXUI_MEDIAMTX_LOG_DATA_PATH":  "/var/lib/mtxui/logs/mediamtx.log",
		"MTXUI_MEDIAMTX_RECORDINGS_DIR": "/var/lib/mtxui/recordings",
		"MTXUI_RECORDINGS_DATA_DIR":     "/var/lib/mtxui/recordings",
		"MTXUI_PASSKEYS":                "off",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.MediaMTXControlHost != "127.0.0.1" || s.MediaMTXRecordingsDir != "/var/lib/mtxui/recordings" ||
		s.MediaMTXLogFile != "/var/lib/mtxui/logs/mediamtx.log" ||
		s.RecordingsDataDir != "/var/lib/mtxui/recordings" {
		t.Fatalf("native local settings not retained: %+v", s)
	}
	if s.DeploymentMode != "native" {
		t.Fatalf("native runtime mode = %q", s.DeploymentMode)
	}
	if got := s.MediaMTXLog(); got != "/var/lib/mtxui/logs/mediamtx.log" {
		t.Fatalf("application log path = %q", got)
	}
	if got := s.RecordingDataDir(); got != "/var/lib/mtxui/recordings" {
		t.Fatalf("application recordings path = %q", got)
	}
	bad := map[string]string{
		"MTXUI_DEPLOYMENT_MODE": "native", "MTXUI_PUBLIC_URL": "http://127.0.0.1:9080",
		"MTXUI_PUBLIC_HOST": "127.0.0.1",
	}
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_MEDIAMTX_CONTROL_HOST must be a loopback IP address in native mode") {
		t.Fatalf("native mode without a private Control API bind accepted: %v", err)
	}
}

func TestRemoteAgentSettings(t *testing.T) {
	kv := minimal()
	kv["MTXUI_MEDIAMTX_AGENT_URL"] = "https://media-agent.example.test:9443"
	kv["MTXUI_MEDIAMTX_AGENT_CA"] = "/etc/mtxui/agent-ca.pem"
	kv["MTXUI_MEDIAMTX_AGENT_CERT"] = "/etc/mtxui/agent-client.pem"
	kv["MTXUI_MEDIAMTX_AGENT_KEY"] = "/etc/mtxui/agent-client-key.pem"
	kv["MTXUI_MEDIAMTX_AGENT_AUTH_URL"] = "http://127.0.0.1:9081/internal/auth"
	kv["MTXUI_REMOTE_AGENT_ID"] = "media-east"
	kv["MTXUI_REMOTE_AGENT_AUTH_KEY"] = strings.Repeat("a7", 32)
	kv["MTXUI_MEDIAMTX_MEDIA_HOST"] = "0.0.0.0"
	kv["MTXUI_MEDIAMTX_WEBRTC_URL"] = "https://media.example.test"
	kv["MTXUI_PUBLIC_MEDIA_URL"] = "https://media.example.test"
	kv["MTXUI_MEDIAMTX_TRUSTED_PROXIES"] = "192.0.2.10"
	s, err := Load(env(kv))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.MediaMTXAPIURL(); got != "https://media-agent.example.test:9443/v1/media" {
		t.Fatalf("agent API URL = %q", got)
	}
	if got := s.MediaMTXAuthURL(); got != kv["MTXUI_MEDIAMTX_AGENT_AUTH_URL"] {
		t.Fatalf("agent auth URL = %q", got)
	}
	if s.MediaMTXControlHost != "" {
		t.Fatalf("unspecified control host should be locked to loopback by remote config seed: %q", s.MediaMTXControlHost)
	}
	if got := s.MediaMTXWebRTCBaseURL(); got != "https://media.example.test" {
		t.Fatalf("remote WebRTC signalling URL = %q", got)
	}
	if got := s.PublicMediaOrigin(); got != "https://media.example.test" {
		t.Fatalf("public media origin = %q", got)
	}

	bad := mapsClone(kv)
	delete(bad, "MTXUI_MEDIAMTX_AGENT_KEY")
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_MEDIAMTX_AGENT_CA, MTXUI_MEDIAMTX_AGENT_CERT, MTXUI_MEDIAMTX_AGENT_KEY") {
		t.Fatalf("missing mTLS key accepted: %v", err)
	}
	bad = mapsClone(kv)
	bad["MTXUI_MEDIAMTX_AGENT_URL"] = "http://media-agent.example.test:9443"
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "must be an https:// origin") {
		t.Fatalf("plain HTTP agent URL accepted: %v", err)
	}
	bad = mapsClone(kv)
	bad["MTXUI_MEDIAMTX_WEBRTC_URL"] = "http://media.example.test"
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_MEDIAMTX_WEBRTC_URL") {
		t.Fatalf("plain HTTP WebRTC signalling URL accepted for remote mode: %v", err)
	}
	bad = mapsClone(kv)
	delete(bad, "MTXUI_PUBLIC_MEDIA_URL")
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_PUBLIC_MEDIA_URL") {
		t.Fatalf("remote mode without a public media origin accepted: %v", err)
	}
	bad = mapsClone(kv)
	bad["MTXUI_PUBLIC_MEDIA_URL"] = "http://media.example.test"
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_PUBLIC_MEDIA_URL") {
		t.Fatalf("plain HTTP public media origin accepted: %v", err)
	}
	bad = mapsClone(kv)
	bad["MTXUI_REMOTE_AGENT_ID"] = "bad\nagent"
	if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "MTXUI_REMOTE_AGENT_ID") {
		t.Fatalf("invalid remote agent identity accepted: %v", err)
	}
}

func mapsClone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestRecordingsRootCannotBeFilesystemRoot(t *testing.T) {
	kv := minimal()
	kv["MTXUI_MEDIAMTX_RECORDINGS_DIR"] = "/"
	if _, err := Load(env(kv)); err == nil || !strings.Contains(err.Error(), "MTXUI_MEDIAMTX_RECORDINGS_DIR must not be the filesystem root") {
		t.Fatalf("filesystem root recordings directory: %v", err)
	}
}

func TestParsing(t *testing.T) {
	kv := minimal()
	kv["MTXUI_PUBLIC_URL"] = "http://10.0.0.5:8080/"
	kv["MTXUI_PUBLIC_HOST"] = "10.0.0.5"
	kv["MTXUI_TRUSTED_PROXIES"] = " 172.29.42.1 , 10.0.0.0/8,fd00::1 "
	kv["MTXUI_STACK_SUBNET"] = "172.29.42.7/24"
	s, err := Load(env(kv))
	if err != nil {
		t.Fatal(err)
	}
	if s.Secure() || s.Origin() != "http://10.0.0.5:8080" {
		t.Errorf("origin %q secure %v", s.Origin(), s.Secure())
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("172.29.42.1/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::1/128"),
	}
	if len(s.TrustedProxies) != len(want) {
		t.Fatalf("trusted = %v", s.TrustedProxies)
	}
	for i := range want {
		if s.TrustedProxies[i] != want[i] {
			t.Errorf("trusted[%d] = %v, want %v", i, s.TrustedProxies[i], want[i])
		}
	}
	if s.StackSubnet != netip.MustParsePrefix("172.29.42.0/24") {
		t.Errorf("stack subnet = %v (should be masked)", s.StackSubnet)
	}
}

func TestRejects(t *testing.T) {
	tests := []struct {
		name, key, value, wantErr string
	}{
		{"missing public URL", "MTXUI_PUBLIC_URL", "", "MTXUI_PUBLIC_URL is required"},
		{"public URL scheme", "MTXUI_PUBLIC_URL", "ftp://x", "must start with http"},
		{"public URL path", "MTXUI_PUBLIC_URL", "https://x.example/ui", "must not have a path"},
		{"public URL credentials", "MTXUI_PUBLIC_URL", "https://a:b@x.example", "credentials"},
		{"public host", "MTXUI_PUBLIC_HOST", "not a host!", "neither a hostname"},
		{"public host label", "MTXUI_PUBLIC_HOST", "-bad.example", "neither a hostname"},
		{"trust everyone v4", "MTXUI_TRUSTED_PROXIES", "0.0.0.0/0", "must not trust every address"},
		{"trust everyone v6", "MTXUI_TRUSTED_PROXIES", "10.0.0.1, ::/0", "must not trust every address"},
		{"trusted garbage", "MTXUI_TRUSTED_PROXIES", "10.0.0.1, nope", `invalid entry "nope"`},
		{"subnet", "MTXUI_STACK_SUBNET", "172.29.42.0", "not a CIDR"},
		{"listen", "MTXUI_LISTEN", "9080", "host:port"},
		{"listen hostname", "MTXUI_LISTEN", "localhost:9080", "IP address"},
		{"same listeners", "MTXUI_INTERNAL_LISTEN", ":9080", "must differ"},
		{"internal URL https", "MTXUI_INTERNAL_URL", "https://sidecar:9081", "plain http"},
		{"internal URL path", "MTXUI_INTERNAL_URL", "http://sidecar:9081/x", "plain http"},
		{"relative data dir", "MTXUI_DATA_DIR", "data", "absolute"},
		{"log level", "MTXUI_LOG_LEVEL", "loud", "debug, info"},
		{"idle timeout", "MTXUI_SESSION_IDLE_TIMEOUT", "1m", "between"},
		{"max age below idle", "MTXUI_SESSION_MAX_AGE", "6h", "at least"},
		{"rate", "MTXUI_LOGIN_RATE_PER_MINUTE", "0", "between 1 and 600"},
		{"join rate", "MTXUI_JOIN_RATE_PER_MINUTE", "0", "between 1 and 600"},
		{"threshold", "MTXUI_LOCKOUT_THRESHOLD", "many", "whole number"},
		{"holding clips past 4 GiB", "MTXUI_HOLDING_MAX_GB", "4", "from 0.01 to 3.9"},
		{"holding clips of nothing", "MTXUI_HOLDING_MAX_GB", "0", "from 0.01 to 3.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv := minimal()
			kv[tt.key] = tt.value
			_, err := Load(env(kv))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestReportsAllProblemsAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{"MTXUI_LOG_LEVEL": "loud"}))
	for _, want := range []string{"MTXUI_PUBLIC_URL is required", "MTXUI_PUBLIC_HOST is required", "MTXUI_LOG_LEVEL"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

// Every declared variable is read by Load, and Load reads nothing undeclared (get panics on that), so the generated
// docs/config.md cannot drift from the code.
func TestVarsMatchLoad(t *testing.T) {
	read := map[string]bool{}
	get := func(k string) string {
		read[k] = true
		return minimal()[k]
	}
	if _, err := Load(get); err != nil {
		t.Fatal(err)
	}
	for _, v := range Vars {
		if !read[v.Name] {
			t.Errorf("%s is declared but never read", v.Name)
		}
		if v.Description == "" {
			t.Errorf("%s has no description", v.Name)
		}
		if v.Required && v.Default != "" {
			t.Errorf("%s is required but has a default", v.Name)
		}
	}
	if len(read) != len(Vars) {
		t.Errorf("Load read %d variables, %d are declared", len(read), len(Vars))
	}
}

// Passkeys need a relying party a browser accepts: a domain name or localhost. auto also needs a secure origin.
func TestPasskeys(t *testing.T) {
	tests := []struct {
		url, mode string
		want      bool
		wantErr   string
	}{
		{"https://mtx.example.com", "auto", true, ""},
		{"http://mtx.example.com", "auto", false, ""},
		{"http://localhost:8080", "auto", true, ""},
		{"https://10.0.0.5", "auto", false, ""},
		{"https://sidecar:9080", "auto", false, ""},
		{"http://ui.e2e.test:9080", "on", true, ""},
		{"https://mtx.example.com", "off", false, ""},
		{"http://sidecar:9080", "on", false, "domain name"},
		{"http://10.0.0.5", "on", false, "domain name"},
	}
	for _, tt := range tests {
		t.Run(tt.mode+" "+tt.url, func(t *testing.T) {
			kv := minimal()
			kv["MTXUI_PUBLIC_URL"], kv["MTXUI_PASSKEYS"] = tt.url, tt.mode
			s, err := Load(env(kv))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.Passkeys != tt.want {
				t.Fatalf("Passkeys = %v, want %v", s.Passkeys, tt.want)
			}
		})
	}
}

func TestTLS(t *testing.T) {
	tests := []struct {
		url, tls, extra string
		wantErr         string
	}{
		{"https://mtx.example.com", "acme", "", ""},
		{"https://mtx.example.com", "off", "", ""},
		{"http://mtx.example.com", "acme", "", "https://"},
		{"https://10.0.0.5", "acme", "", "domain name"},
		{"https://localhost", "acme", "", "domain name"},
		{"https://mtx.example.com", "maybe", "", "one of off, acme"},
		{"https://mtx.example.com", "acme", "MTXUI_HTTP_LISTEN=:9080", "must differ"},
		{"https://mtx.example.com", "acme", "MTXUI_ACME_DIRECTORY=http://ca.example/dir", "https:// URL"},
		{"https://mtx.example.com", "acme", "MTXUI_ACME_CA_CERT=ca.pem", "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.tls+" "+tt.url+" "+tt.extra, func(t *testing.T) {
			kv := minimal()
			kv["MTXUI_PUBLIC_URL"], kv["MTXUI_TLS"] = tt.url, tt.tls
			if k, v, ok := strings.Cut(tt.extra, "="); ok {
				kv[k] = v
			}
			s, err := Load(env(kv))
			if tt.wantErr == "" {
				if err != nil || s.TLS != tt.tls {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
