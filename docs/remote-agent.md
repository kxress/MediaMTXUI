# Remote MediaMTX agent

The remote agent lets a native MediaMTXUI control plane manage one MediaMTX host over a private, authenticated link. The MediaMTX Control API remains bound to loopback on the media host. The agent exposes only the Control API operations listed in `sidecar/internal/mtxapi/operations.yaml`, one fixed configuration file, a health response, and a local authentication callback. It does not run commands supplied by MediaMTXUI or expose general file access.

The current implementation supports one configured remote agent per MediaMTXUI process. The remote config adapter and agent identity are explicit so a later server registry can support multiple independent MediaMTX instances.

## Transport and trust

The agent management endpoint uses TLS 1.2 or later and requires a client certificate. It verifies the certificate chain and pins the configured client certificate SHA-256 fingerprint. Use a CA and client identity dedicated to this control-plane connection. Bind `MTXUI_AGENT_LISTEN` to a private management address and restrict that port to the MediaMTXUI host with the network controls available in your environment. The default bind is loopback.

MediaMTX's authentication callback stays on the media host at `http://127.0.0.1:9081/internal/auth`. The agent forwards each callback to the MediaMTXUI HTTPS endpoint with an HMAC signature, a short timestamp window and a one-time nonce. The key is a random 32-byte value shared by the two processes. Bad signatures, expired requests, replayed requests and connection failures are denied. The callback retains the existing account, stream-key, role, source-address, guest-ticket, expiry and revocation checks.

The agent can change only the configured MediaMTX YAML file. Writes require the current SHA-256, pass the same MediaMTX rules and the host's installed `mediamtx --validate-conf`, then use an atomic rename. MediaMTXUI stores the history centrally, verifies effective settings through the Control API and restores the previous content if MediaMTX stops answering after a change. External valid edits are adopted into central history; invalid edits are restored from the last good snapshot.

## Install the agent

Run the installer on the MediaMTX host. It installs the same native executable and systemd unit without installing or starting the dashboard or MediaMTX service:

```sh
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install-native-agent.sh | sudo bash
```

Set `--repository owner/repository` through a downloaded script for a fork, and use `--version vX.Y.Z` to pin a release. The installer verifies the release checksum. It leaves `mtxui-agent.service` disabled until its environment and certificates are configured.

Edit `/etc/mtxui/agent.env` on the media host. Point `MTXUI_AGENT_TLS_CERT`, `MTXUI_AGENT_TLS_KEY`, and `MTXUI_AGENT_CLIENT_CA` to the agent server certificate, private key and management client CA. Set the client certificate fingerprint to the SHA-256 of the exact MediaMTXUI client certificate's DER encoding:

```sh
openssl x509 -in management-client.pem -outform DER | sha256sum
```

Create one HMAC key and set the same 64-character hex value as `MTXUI_AGENT_AUTH_KEY` on the agent and `MTXUI_REMOTE_AGENT_AUTH_KEY` on MediaMTXUI. Generate it with:

```sh
openssl rand -hex 32
```

Set the same `MTXUI_AGENT_ID` and `MTXUI_REMOTE_AGENT_ID` on both hosts. Set `MTXUI_AGENT_AUTH_URL` to the MediaMTXUI HTTPS callback URL ending in `/agent/v1/auth`. Set `MTXUI_AGENT_LISTEN` to a private interface address reachable from MediaMTXUI. Keep `MTXUI_AGENT_API_URL` and `MTXUI_AGENT_AUTH_LISTEN` on loopback.

Set `MTXUI_AGENT_CONFIG_PATH` to the MediaMTX YAML file and `MTXUI_AGENT_MEDIAMTX_BIN` to the installed validator binary. `MTXUI_AGENT_RECORDINGS_DIR` must match the recording root in MediaMTX configuration. The agent runs as the unprivileged `mtxui` account. Grant it read/write access only to the configured YAML file and its parent directory, read access to the validator binary and access to its configured temporary directory. If the config file is outside `/var/lib/mtxui-agent`, add its directory to the systemd unit's `ReadWritePaths` with a drop-in. The agent atomically replaces the config with mode `0640` owned by its `mtxui` group; add the MediaMTX service account to that group, and ensure it can traverse the parent directories, so MediaMTX can read later updates.

Configure MediaMTX to use the local callback and keep administrative listeners private:

```yaml
authMethod: http
authHTTPAddress: http://127.0.0.1:9081/internal/auth
authHTTPExclude: []
api: yes
apiAddress: 127.0.0.1:9997
metrics: yes
metricsAddress: 127.0.0.1:9998
playback: yes
playbackAddress: 127.0.0.1:9996
```

The MediaMTXUI configuration manager enforces those private binds and the exact callback address on every candidate. Keep the MediaMTX WebRTC HTTP listener on loopback when a TLS reverse proxy runs on the media host, or bind it to the private interface used by a separate proxy. Advertise the public media hostname with `webrtcAdditionalHosts`; allow WebRTC UDP media traffic directly between browsers and MediaMTX. Do not publish the Control API, metrics or playback listener.

After certificates, callback configuration, config path and filesystem permissions are ready, start the service:

```sh
sudo systemctl enable --now mtxui-agent.service
sudo journalctl -u mtxui-agent.service
```

## Configure MediaMTXUI

On the MediaMTXUI host, set these variables in its existing environment file. The paths are local to the MediaMTXUI host. The callback URL and ID must agree with the remote agent. `MTXUI_MEDIAMTX_TRUSTED_PROXIES` contains the HTTPS proxy address MediaMTX sees; this preserves the real publisher and viewer address in authorization checks.

```sh
MTXUI_MEDIAMTX_AGENT_URL=https://media-agent.example.net:9443
MTXUI_MEDIAMTX_AGENT_CA=/etc/mtxui/agent-ca.pem
MTXUI_MEDIAMTX_AGENT_CERT=/etc/mtxui/management-client.pem
MTXUI_MEDIAMTX_AGENT_KEY=/etc/mtxui/management-client-key.pem
MTXUI_MEDIAMTX_AGENT_AUTH_URL=http://127.0.0.1:9081/internal/auth
MTXUI_REMOTE_AGENT_ID=media-east
MTXUI_REMOTE_AGENT_AUTH_KEY=<same 64-character hex key as the agent>

# MediaMTX bind address for HLS and WebRTC signalling; loopback when the TLS reverse proxy is on this host.
MTXUI_MEDIAMTX_MEDIA_HOST=127.0.0.1
# HTTPS origin MediaMTXUI uses for its own WebRTC signaling proxy. It may be an internal HTTPS origin.
MTXUI_MEDIAMTX_WEBRTC_URL=https://media.example.net
# Browser-reachable HTTPS origin for the direct MediaMTX page, WHIP and WHEP URLs.
MTXUI_PUBLIC_MEDIA_URL=https://media.example.net
# Address of Caddy as seen by MediaMTX on this host (replace if the proxy is elsewhere).
MTXUI_MEDIAMTX_TRUSTED_PROXIES=127.0.0.1/32
```

`MTXUI_PUBLIC_URL` is the management UI origin and can resolve only inside the VPN. `MTXUI_PUBLIC_HOST` is the public media host advertised to stream clients and in ICE candidates. `MTXUI_PUBLIC_MEDIA_URL` must be an HTTPS origin reachable by viewers and OBS; it serves MediaMTX's own page and direct WHIP/WHEP signaling. `MTXUI_MEDIAMTX_WEBRTC_URL` is the HTTPS origin MediaMTXUI uses for its separate dashboard WebRTC proxy. They can be the same URL or different URLs. The reverse proxy must forward the WebRTC HTTP listener and add `X-Robots-Tag: noindex, nofollow, noarchive`; the MediaMTX HTTP listener must allow only the configured media origin for WebRTC browser requests. The proxy must not expose the Control API, metrics or playback listener. Set `MTXUI_MEDIAMTX_TRUSTED_PROXIES` to the proxy address MediaMTX sees, so MediaMTX can safely recover viewer addresses from `X-Forwarded-For`.

For example, when Caddy runs on the media host and forwards HTTPS to MediaMTX's loopback WebRTC listener, a site block can look like this:

```caddyfile
media.example.net {
  header X-Robots-Tag "noindex, nofollow, noarchive"
  reverse_proxy 127.0.0.1:8889
}
```

This serves the viewer page at `https://media.example.net/<stream-path>` and direct OBS WHIP at
`https://media.example.net/<stream-path>/whip`. The proxy handles only the HTTPS page and signaling requests. Open
UDP 8189 from viewers and publishers to the MediaMTX host so ICE media travels directly; if that UDP route is blocked,
configure a reachable TURN service. Open only the ingest ports you use (for example RTMP TCP 1935) in addition.
`X-Robots-Tag` discourages compliant search engines from indexing the pages; anyone with a link can still watch a
public stream. Do not add those pages to public links or sitemaps.

`MTXUI_MEDIAMTX_WEBRTC_URL` must use HTTPS and can terminate on MediaMTX's own TLS listener or an HTTPS reverse proxy. The dashboard forwards SDP and session-control requests over this encrypted connection; ICE and RTP/RTCP media still travel directly between browsers and MediaMTX. Keep the agent endpoint separate and on the private management route. Admins and streamers reach the UI through the VPN; viewers and OBS reach only the public MediaMTX endpoints. The media agent still needs a private route to the UI's signed authorization callback. Do not set `MTXUI_EXPOSURE_CONTROL=on` in remote mode.

Restart MediaMTXUI after installing its client CA and certificate. Its config editor will reconcile the remote YAML through the agent. Existing Docker and native-local installs do not need any of these variables.

## Remote feature boundaries

The dashboard's own watch page sends WebRTC offers, answers and trickle signaling through MediaMTXUI to the configured HTTPS origin; ICE candidates and RTP/RTCP media travel between the browser and MediaMTX. A shared public watch link and OBS WHIP address use `MTXUI_PUBLIC_MEDIA_URL` and do not contact the dashboard. Remote mode disables the dashboard's HLS fallback because the existing implementation sends HLS segments through the dashboard host. If a browser cannot establish WebRTC, the player explains that HLS is disabled.

Recording file playback/export, local MediaMTX log tailing, disk-budget enforcement and holding-clip uploads are disabled in remote mode. Recordings and logs remain on the media host. The MediaMTX Control API still reports active sessions and stream statistics. Configuration can enable recording on the media host, but the dashboard cannot inspect or prune those files yet. These limits are returned in the health capability fields and are shown by affected API routes.

If the agent is unavailable, ongoing WebRTC media sessions remain between clients and MediaMTX. MediaMTX rejects new authorization callbacks when it cannot reach the signed callback service. The dashboard remains available and distinguishes an unavailable agent from an unavailable MediaMTX Control API; either way, it does not claim that active media has stopped. It uses the last saved config snapshot only for read-only display. Remote config writes fail closed until the agent reconnects. Browser WebRTC failures are reported by the player as media-transport failures.

## Current limits

- One remote MediaMTX instance is configured per MediaMTXUI process; multiple-agent administration is not yet exposed in the dashboard.
- Agent enrollment and certificate issuance are operator-managed. The agent does not create users, certificates or trust relationships.
- MediaMTX log, recording-file and holding-media operations stay local until narrow agent operations are implemented for them.
