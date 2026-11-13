#!/usr/bin/env bash
set -euo pipefail

purge=false
if [[ ${1:-} == --purge ]]; then
  purge=true
elif [[ $# -gt 0 ]]; then
  echo "Usage: uninstall-native.sh [--purge]" >&2
  exit 2
fi
[[ $(id -u) == 0 ]] || { echo "Run this uninstaller as root (for example, with sudo)." >&2; exit 1; }
command -v systemctl >/dev/null || { echo "A systemd Linux host is required." >&2; exit 1; }

systemctl disable --now mtxui-agent.service mtxui-mediamtx.service mtxui.service 2>/dev/null || true
rm -f /usr/lib/systemd/system/mtxui.service /usr/lib/systemd/system/mtxui-mediamtx.service /usr/lib/systemd/system/mtxui-agent.service
rm -rf /usr/libexec/mtxui
rm -f /usr/local/bin/mtxui
rm -f /usr/share/mtxui/mtxui.env.example
rm -f /usr/share/mtxui/mtxui-agent.env.example
rmdir /usr/share/mtxui 2>/dev/null || true
systemctl daemon-reload

if [[ $purge == true ]]; then
  rm -rf /var/lib/mtxui /etc/mtxui
  echo "MediaMTX UI, its configuration and its default data directory were removed."
  echo "The mtxui system account was kept; remove it separately if it is no longer needed."
else
  echo "Services and executables were removed. Configuration in /etc/mtxui and data in /var/lib/mtxui were kept."
  echo "Pass --purge to remove those default directories too. Customized MTXUI_DATA_DIR paths must be removed separately."
fi
