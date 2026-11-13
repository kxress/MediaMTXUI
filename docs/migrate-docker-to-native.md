# Move a Compose installation to native Linux services

This procedure copies a Compose installation's management database and media files into the native layout. It keeps the Compose containers and volumes available for rollback. Run it first against a copy of your installation and verify the native services before scheduling a production cutover.

The native app preserves accounts, stream definitions, credentials, audit history and configuration snapshots when the complete `/data/state` directory is copied. Do not copy only the SQLite database: the credential encryption key beside it is required to read existing secrets.

## 1. Prepare the native host

Install the same MediaMTXUI release as the Compose installation, then stop the native services so the copied state is not open while it is replaced:

```sh
sudo systemctl stop mtxui.service mtxui-mediamtx.service
```

Keep a copy of the Compose `.env`, `compose.yaml`, any override files and the encrypted backup. Do not run `docker compose down -v`; that deletes the named volumes needed for copying and rollback.

## 2. Stop Compose and copy its data

From the directory containing `compose.yaml`, stop the stack. `stop` keeps its containers and volumes so `docker compose cp` can read them:

```sh
docker compose stop
mkdir -m 700 migration
docker compose cp sidecar:/data migration/data
docker compose cp sidecar:/holding migration/holding
```

Copy the files into the native data directory and set ownership for the unprivileged native service account:

```sh
sudo mkdir -p /var/lib/mtxui
sudo cp -a migration/data/. /var/lib/mtxui/
sudo mkdir -p /var/lib/mtxui/holding
sudo cp -a migration/holding/. /var/lib/mtxui/holding/
sudo chown -R mtxui:mtxui /var/lib/mtxui
sudo chmod 0700 /var/lib/mtxui/state
```

The `/data` copy includes the management state, managed config, recording data, MediaMTX logs, hooks and backups. If Compose used a custom `RECORDINGS_PATH`, the contents are still copied from the sidecar's `/data/recordings` mount.

## 3. Configure native paths and listeners

Edit `/etc/mtxui/mtxui.env`. Keep the public URL and public stream hostname from the Compose configuration, then set the local paths and loopback listeners to match the files copied above:

```ini
MTXUI_PUBLIC_URL=https://ui.example.net
MTXUI_PUBLIC_HOST=media.example.net
MTXUI_LISTEN=127.0.0.1:9080
MTXUI_INTERNAL_LISTEN=127.0.0.1:9081
MTXUI_INTERNAL_URL=http://127.0.0.1:9081
MTXUI_TRUSTED_PROXIES=127.0.0.1/32
MTXUI_STACK_SUBNET=127.0.0.1/32
MTXUI_MEDIAMTX_HOST=127.0.0.1
MTXUI_MEDIAMTX_CONTROL_HOST=127.0.0.1
MTXUI_DATA_DIR=/var/lib/mtxui
MTXUI_HOLDING_DIR=/var/lib/mtxui/holding
MTXUI_MEDIAMTX_LOG_PATH=/var/lib/mtxui/logs/mediamtx.log
MTXUI_MEDIAMTX_LOG_DATA_PATH=/var/lib/mtxui/logs/mediamtx.log
MTXUI_MEDIAMTX_RECORDINGS_DIR=/var/lib/mtxui/recordings
MTXUI_RECORDINGS_DATA_DIR=/var/lib/mtxui/recordings
```

Set `MTXUI_TRUSTED_PROXIES` to the actual address or CIDR of the reverse proxy in front of the UI; the loopback value above is only correct when that proxy connects from loopback. The native systemd unit selects native mode, which requires a loopback Control API bind.

## 4. Update the copied MediaMTX configuration

Before starting the services, edit `/var/lib/mtxui/config/mediamtx.yml` to remove Compose-only addresses and paths. Preserve stream/path definitions and authentication settings, and make these changes:

- Set `authHTTPAddress` to `http://127.0.0.1:9081/internal/auth`.
- Bind `apiAddress`, `metricsAddress` and `playbackAddress` to `127.0.0.1` on their existing ports.
- Bind `hlsAddress` and `webrtcAddress` to `127.0.0.1` on their existing ports. The UI reaches these local signaling endpoints; WebRTC media still goes directly between clients and MediaMTX.
- Change `logFile` from `/logs/...` to `/var/lib/mtxui/logs/...`.
- Change every `recordPath` from `/recordings/...` to `/var/lib/mtxui/recordings/...`, including any per-path overrides.
- Change any file source or hook paths from `/holding/...` or `/hooks/...` to the matching `/var/lib/mtxui/holding/...` or `/var/lib/mtxui/hooks/...` path.

Validate the edited config before starting either service:

```sh
sudo -u mtxui /usr/libexec/mtxui/mediamtx --validate-conf=/var/lib/mtxui/config/mediamtx.yml
```

The app validates it again at startup. A valid edited config is recorded as a new configuration snapshot; prior snapshots remain in the copied database.

## 5. Start and verify

```sh
sudo systemctl start mtxui.service mtxui-mediamtx.service
systemctl status mtxui.service mtxui-mediamtx.service
sudo journalctl -u mtxui.service -u mtxui-mediamtx.service
```

Sign in with the existing account. Verify stream settings, credentials, recording visibility, and holding clips. Test a publisher and viewer from another device, and check that the host's Control API, metrics and playback ports listen only on loopback. Keep the Compose containers and volumes stopped until the native installation has been verified.

## Roll back

Stop the native services, restore the original Compose environment and files, then start the existing Compose stack:

```sh
sudo systemctl stop mtxui.service mtxui-mediamtx.service
docker compose up -d
```

The native copy may have newer state or configuration after cutover. Do not delete either set of data during rollback; reconcile any changes before attempting another migration.
