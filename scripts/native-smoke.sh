#!/usr/bin/env bash
set -euo pipefail

root=${1:-}
if [[ -z $root || ! -x $root/usr/libexec/mtxui/mtxui || ! -x $root/usr/libexec/mtxui/mediamtx ]]; then
  echo "Usage: native-smoke.sh <native-rootfs>" >&2
  exit 2
fi
if [[ $(id -u) == 0 ]]; then
  echo "Run this smoke test as an unprivileged user." >&2
  exit 1
fi
command -v curl >/dev/null || { echo "curl is required." >&2; exit 1; }

app=$root/usr/libexec/mtxui/mtxui
media=$root/usr/libexec/mtxui/mediamtx
tmp=$(mktemp -d)
data=$tmp/data
media_data=$tmp/media
mkdir -p "$data"
umask 077
ui_pid=
media_pid=
cleanup() {
  set +e
  if [[ -n $media_pid ]]; then kill -INT "$media_pid" 2>/dev/null; wait "$media_pid" 2>/dev/null; fi
  if [[ -n $ui_pid ]]; then kill -TERM "$ui_pid" 2>/dev/null; wait "$ui_pid" 2>/dev/null; fi
  rm -rf "$tmp"
}
trap cleanup EXIT

export MTXUI_PUBLIC_URL=http://127.0.0.1:19080
export MTXUI_DEPLOYMENT_MODE=native
export MTXUI_PUBLIC_HOST=127.0.0.1
export MTXUI_LISTEN=127.0.0.1:19080
export MTXUI_INTERNAL_LISTEN=127.0.0.1:19081
export MTXUI_INTERNAL_URL=http://127.0.0.1:19081
export MTXUI_STACK_SUBNET=127.0.0.1/32
export MTXUI_MEDIAMTX_HOST=127.0.0.1
export MTXUI_MEDIAMTX_CONTROL_HOST=127.0.0.1
export MTXUI_DATA_DIR=$data
export MTXUI_HOLDING_DIR=$data/holding
export MTXUI_MEDIAMTX_LOG_PATH=$media_data/logs/mediamtx.log
export MTXUI_MEDIAMTX_LOG_DATA_PATH=$media_data/logs/mediamtx.log
export MTXUI_MEDIAMTX_RECORDINGS_DIR=$media_data/recordings
export MTXUI_RECORDINGS_DATA_DIR=$media_data/recordings
export MTXUI_MEDIAMTX_BIN=$media
export MTXUI_PASSKEYS=off

"$app" serve >"$tmp/ui.log" 2>&1 &
ui_pid=$!
for _ in {1..100}; do
  if curl -fsS http://127.0.0.1:19081/healthz >/dev/null 2>&1; then break; fi
  sleep 0.1
done
if ! curl -fsS http://127.0.0.1:19081/healthz >/dev/null || ! curl -fsS http://127.0.0.1:19080/ >/dev/null; then
  cat "$tmp/ui.log" >&2
  exit 1
fi
token=$("$app" setup-token)
[[ -n $token && -f $data/state/setup-token ]]
grep -Fq 'apiAddress: "127.0.0.1:9997"' "$data/config/mediamtx.yml"
grep -Fq "logFile: \"$media_data/logs/mediamtx.log\"" "$data/config/mediamtx.yml"
grep -Fq "recordPath: \"$media_data/recordings/%path/" "$data/config/mediamtx.yml"
"$media" --validate-conf="$data/config/mediamtx.yml" >/dev/null

"$media" "$data/config/mediamtx.yml" >"$tmp/mediamtx.log" 2>&1 &
media_pid=$!
for _ in {1..100}; do
  if grep -Fq '[API] started with listener on 127.0.0.1:9997' "$tmp/mediamtx.log"; then break; fi
  if ! kill -0 "$media_pid" 2>/dev/null; then cat "$tmp/mediamtx.log" >&2; exit 1; fi
  sleep 0.1
done
if ! grep -Fq '[API] started with listener on 127.0.0.1:9997' "$tmp/mediamtx.log"; then
  cat "$tmp/mediamtx.log" >&2
  exit 1
fi
for _ in {1..100}; do
  if grep -Fq '"reachable":true' "$tmp/ui.log"; then break; fi
  sleep 0.1
done
if ! grep -Fq '"reachable":true' "$tmp/ui.log"; then
  cat "$tmp/ui.log" >&2
  exit 1
fi

kill -TERM "$ui_pid"
if wait "$ui_pid"; then ui_status=0; else ui_status=$?; fi
ui_pid=
[[ $ui_status == 0 ]] || { cat "$tmp/ui.log" >&2; echo "UI did not shut down cleanly (exit $ui_status)." >&2; exit 1; }
echo "native smoke passed: unprivileged startup, custom paths, MediaMTX validation, health, API reachability and graceful UI shutdown"
