# Native Linux installation

Native installation runs the MediaMTX UI executable and MediaMTX as separate, unprivileged systemd services. Docker is not installed or used. The UI owns the management database and writes the local MediaMTX configuration; the media service reads that file and serves media directly.

The release workflow attaches native archives for Linux amd64 and arm64. Each archive includes the matching MediaMTX binary used for configuration validation, the systemd units, and a loopback-only starting configuration. The release also includes `SHA256SUMS`.

## Install or upgrade

On Debian or Ubuntu, install `curl` and `coreutils` if needed:

```sh
sudo apt-get update
sudo apt-get install -y curl coreutils
```

On Fedora, the equivalent packages are available with `dnf`:

```sh
sudo dnf install -y curl coreutils
```

Run the installer as root. It verifies the release archive checksum, installs an `mtxui` system account, installs binaries and units, and starts or restarts both services. The first install creates `/etc/mtxui/mtxui.env`; upgrades preserve it and the application data.

```sh
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install-native.sh | sudo bash
```

Choose a specific release or a fork's release repository with:

```sh
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install-native.sh | sudo bash -s -- --version v1.4.1
```

The installer accepts `MTXUI_REPOSITORY=owner/repository` to select a fork. Check the archive checksum and release signature according to your organization's release policy before installing.

## Configure the service

The initial settings bind the dashboard and MediaMTX's private HTTP listeners to loopback. This keeps the dashboard private while you configure it. Edit `/etc/mtxui/mtxui.env` and set:

```ini
MTXUI_PUBLIC_URL=https://ui.example.net
MTXUI_PUBLIC_HOST=media.example.net
MTXUI_LISTEN=127.0.0.1:9080
MTXUI_TRUSTED_PROXIES=127.0.0.1/32
MTXUI_STACK_SUBNET=127.0.0.1/32
```

Put a reverse proxy with a valid HTTPS certificate in front of `127.0.0.1:9080`. Set `MTXUI_TRUSTED_PROXIES` to the proxy's actual source address or CIDR. Do not trust every address. The app and MediaMTX must agree on their public addresses; the UI origin and media hostname can be different.

For a private LAN deployment, use an HTTPS reverse proxy trusted by the client devices. Plain HTTP is suitable only for local testing; it does not protect account credentials in transit.

The default local media settings keep the Control API, metrics, playback and HLS/WebRTC signaling bound to `127.0.0.1`. RTSP, RTMP, SRT and WebRTC media listeners remain on MediaMTX's configured protocol addresses. Configure the host firewall for the protocols you actually enable.

The settings file can also set `MTXUI_DATA_DIR`, `MTXUI_HOLDING_DIR`, `MTXUI_MEDIAMTX_LOG_PATH`, `MTXUI_MEDIAMTX_LOG_DATA_PATH`, `MTXUI_MEDIAMTX_RECORDINGS_DIR`, and `MTXUI_RECORDINGS_DATA_DIR`. The `*_DATA_PATH`/`*_DATA_DIR` values are the paths the UI reads; the MediaMTX values are the paths MediaMTX sees. Set each pair to the same host directory for native local operation. For the bundled local systemd services, keep the MediaMTX configuration file at `$MTXUI_DATA_DIR/config/mediamtx.yml`; the MediaMTX unit reads `MTXUI_DATA_DIR` from the same environment file. MediaMTX and the UI run as the `mtxui` system user, and local recording/log/holding directories are shared by path on that host.

After changing settings, restart both services:

```sh
sudo systemctl restart mtxui.service mtxui-mediamtx.service
```

Check service health and logs:

```sh
systemctl status mtxui.service mtxui-mediamtx.service
journalctl -u mtxui.service -u mtxui-mediamtx.service
```

Read the first-run token:

```sh
sudo -u mtxui env MTXUI_DATA_DIR=/var/lib/mtxui /usr/libexec/mtxui/mtxui setup-token
```

Then open the dashboard URL, enter the token, and create the first administrator. The management database, credential key, configuration snapshots, recordings, logs, and holding clips are under `/var/lib/mtxui` by default. Keep this directory and `/etc/mtxui/mtxui.env` when upgrading or restoring the service.

## Service and file layout

| Path | Purpose |
|---|---|
| `/usr/libexec/mtxui/mtxui` | Standalone Go executable with embedded web frontend |
| `/usr/local/bin/mtxui` | CLI wrapper that uses the native default data directory |
| `/usr/libexec/mtxui/mediamtx` | Pinned MediaMTX binary used for validation and local serving |
| `/etc/mtxui/mtxui.env` | Service configuration; retained across upgrades |
| `/var/lib/mtxui/state/` | SQLite management state and generated secrets |
| `/var/lib/mtxui/config/mediamtx.yml` | Managed MediaMTX configuration |
| `/var/lib/mtxui/recordings/` | Local recording files |
| `/var/lib/mtxui/logs/` | MediaMTX log |
| `/var/lib/mtxui/holding/` | Holding-screen files |

The default systemd services are `mtxui.service` and `mtxui-mediamtx.service`. Both run with the unprivileged `mtxui` user. The UI handles SIGTERM and closes its listeners and background work during shutdown.

## Uninstall

The uninstaller stops and removes the units and executables while keeping configuration and data:

```sh
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/uninstall-native.sh | sudo bash
```

Pass `--purge` to remove `/etc/mtxui` and the default `/var/lib/mtxui` data directory. If `MTXUI_DATA_DIR` was changed, remove that custom directory separately after retaining anything you need. The uninstaller keeps the `mtxui` system account so preserved files retain their owner.

## Manage MediaMTX on another host

Native MediaMTXUI can manage a MediaMTX server on a different Linux host through the optional authenticated agent. This does not require shared Docker networks or filesystems, and the MediaMTX Control API stays bound to loopback. Follow the [remote agent setup](remote-agent.md) for certificate provisioning, callback configuration and firewall requirements. Setting `MTXUI_MEDIAMTX_HOST` to a remote address by itself is not remote management.

The first distributed release supports one selected remote instance per MediaMTXUI process. WebRTC media travels directly between browsers and MediaMTX; remote HLS fallback, recording-file export, media log viewing and holding-clip upload are reported as unavailable because they require media-host-local storage access. The remote setup guide lists these boundaries and recovery behavior.

To preserve a Compose installation's accounts, streams and local files while changing to native services, follow the [Compose to native migration guide](migrate-docker-to-native.md).
