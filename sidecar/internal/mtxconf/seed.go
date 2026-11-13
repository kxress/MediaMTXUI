// Package mtxconf renders, validates and writes mediamtx.yml. Nothing reaches the file unvalidated: MediaMTX exits
// on an invalid reload and reloads its open defaults from an empty file, so every candidate passes the
// sidecar's rules and `mediamtx --validate-conf` before an atomic rename puts it in place.
package mtxconf

import (
	"bytes"
	"encoding/json"
	"net"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"text/template"
)

// SeedParams parameterize the config the sidecar writes on first start and at setup.
type SeedParams struct {
	MediaMTXVersion string
	AuthURL         string         // the sidecar's internal /internal/auth
	PublicHost      string         // offered to WebRTC clients
	WebRTCOrigin    string         // the one browser origin allowed to use MediaMTX's WebRTC HTTP endpoints
	StackSubnet     netip.Prefix   // trusted proxies for HLS, WebRTC and playback; omitted when unknown
	TrustedProxies  []netip.Prefix // explicit trusted proxy addresses for a remote MediaMTX instance
	ControlHost     string         // bind host for private HTTP listeners; empty preserves Docker's all-interface bind
	MediaHost       string         // bind host for HLS and WebRTC signalling; empty follows ControlHost
	LogFile         string         // path as seen by MediaMTX
	RecordingsDir   string         // path as seen by MediaMTX
	RTSP, RTMP, SRT bool           // ingest protocols; all off until setup chooses
}

var seedTemplate = template.Must(template.New("seed").Funcs(template.FuncMap{
	"q": func(s string) string { b, _ := json.Marshal(s); return string(b) }, // JSON strings are valid YAML scalars
	"yesno": func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	},
	"bind": func(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) },
	"mediaHost": func(media, control string) string {
		if media != "" {
			return media
		}
		return control
	},
	"proxies": func(stack netip.Prefix, extra []netip.Prefix) []string {
		out := make([]string, 0, len(extra)+1)
		if stack.IsValid() {
			out = append(out, stack.String())
		}
		for _, prefix := range extra {
			if !prefix.IsValid() || slices.Contains(out, prefix.String()) {
				continue
			}
			out = append(out, prefix.String())
		}
		return out
	},
	"pathJoin": func(root, suffix string) string { return path.Join(root, suffix) },
}).Parse(`# MediaMTX configuration, managed by the MediaMTX UI sidecar.
#
# Change settings in the UI: the sidecar validates every change before MediaMTX sees it. MediaMTX stops when it
# reloads an invalid file, so hand edits are risky, and the sidecar reverts edits that fail validation.
# Anything not set here keeps MediaMTX's default:
# https://github.com/bluenviron/mediamtx/blob/v{{.MediaMTXVersion}}/mediamtx.yml

###############################################
# Logs

logLevel: info
logDestinations: [stdout, file]
logFile: {{q .LogFile}}

###############################################
# Authentication: the sidecar decides every request.

authMethod: http
authHTTPAddress: {{q .AuthURL}}
authHTTPExclude: []

###############################################
# Control API, metrics and playback: use the configured private bind host.

{{- $trusted := proxies .StackSubnet .TrustedProxies}}

api: yes
apiAddress: {{q (bind .ControlHost 9997)}}
metrics: yes
metricsAddress: {{q (bind .ControlHost 9998)}}
playback: yes
playbackAddress: {{q (bind .ControlHost 9996)}}
{{- if $trusted}}
playbackTrustedProxies: [{{range $i, $p := $trusted}}{{if $i}}, {{end}}{{q $p}}{{end}}]
{{- end}}
pprof: no

###############################################
# Protocols. RTSP, RTMP and SRT carry streams in and out; switch them on in the UI.

rtsp: {{yesno .RTSP}}
rtspTransports: [tcp]
rtmp: {{yesno .RTMP}}
srt: {{yesno .SRT}}

# MediaMTX serves HLS and WebRTC signalling; WebRTC media uses UDP 8189.
hls: yes
hlsAddress: {{q (bind (mediaHost .MediaHost .ControlHost) 8888)}}
{{- if $trusted}}
hlsTrustedProxies: [{{range $i, $p := $trusted}}{{if $i}}, {{end}}{{q $p}}{{end}}]
{{- end}}
webrtc: yes
webrtcAddress: {{q (bind (mediaHost .MediaHost .ControlHost) 8889)}}
{{- if $trusted}}
webrtcTrustedProxies: [{{range $i, $p := $trusted}}{{if $i}}, {{end}}{{q $p}}{{end}}]
{{- end}}
webrtcIPsFromInterfaces: no
webrtcAdditionalHosts: [{{q .PublicHost}}]
{{- if .WebRTCOrigin}}
webrtcAllowOrigins: [{{q .WebRTCOrigin}}]
{{- end}}
moq: no

###############################################
# Path defaults

pathDefaults:
  recordPath: {{q (pathJoin .RecordingsDir "%path/%Y-%m-%d_%H-%M-%S-%f_%z")}}
  recordDeleteAfter: 168h

###############################################
# Paths. Any path name may be used; stream credentials decide who may publish or read where.

paths:
  all_others:
`))

// Seed renders the sidecar's mediamtx.yml.
func Seed(p SeedParams) ([]byte, error) {
	if p.LogFile == "" {
		p.LogFile = "/logs/mediamtx.log"
	}
	if p.RecordingsDir == "" {
		p.RecordingsDir = "/recordings"
	}
	var b bytes.Buffer
	if err := seedTemplate.Execute(&b, p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
