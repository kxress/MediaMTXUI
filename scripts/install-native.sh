#!/usr/bin/env bash
set -euo pipefail

repo=${MTXUI_REPOSITORY:-greatmastix/MediaMTXUI}
version=latest
while (($#)); do
  case $1 in
    --version)
      (($# >= 2)) || { echo "install-native.sh: --version needs a release tag" >&2; exit 2; }
      version=$2
      shift 2
      ;;
    --repository)
      (($# >= 2)) || { echo "install-native.sh: --repository needs owner/repository" >&2; exit 2; }
      repo=$2
      shift 2
      ;;
    -h|--help)
      echo "Usage: install-native.sh [--version vX.Y.Z]"
      echo "Use --repository owner/repository to select a fork's releases."
      exit 0
      ;;
    *) echo "install-native.sh: unknown option: $1" >&2; exit 2 ;;
  esac
done

[[ $(id -u) == 0 ]] || { echo "Run this installer as root (for example, with sudo)." >&2; exit 1; }
[[ $(uname -s) == Linux ]] || { echo "Native releases support Linux only." >&2; exit 1; }
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m) (supported: amd64, arm64)." >&2; exit 1 ;;
esac
command -v curl >/dev/null || { echo "Install curl and try again." >&2; exit 1; }
command -v sha256sum >/dev/null || { echo "Install coreutils (sha256sum) and try again." >&2; exit 1; }
command -v systemctl >/dev/null || { echo "A systemd Linux host is required." >&2; exit 1; }

if [[ $version == latest ]]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi
asset="mtxui-linux-$arch.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
(cd "$tmp" && grep -E "^[[:xdigit:]]{64}  $asset$" SHA256SUMS >CHECKSUM && sha256sum --check CHECKSUM)
tar -tzf "$tmp/$asset" >"$tmp/archive.list"
grep -q '^usr/libexec/mtxui/mtxui$' "$tmp/archive.list"
grep -q '^usr/libexec/mtxui/mediamtx$' "$tmp/archive.list"
grep -q '^usr/lib/systemd/system/mtxui-agent.service$' "$tmp/archive.list"
tar -xzf "$tmp/$asset" -C "$tmp"

if ! getent group mtxui >/dev/null; then
  groupadd --system mtxui
fi
if ! id mtxui >/dev/null 2>&1; then
  useradd --system --gid mtxui --home-dir /var/lib/mtxui --create-home --shell /usr/sbin/nologin mtxui
fi
install -d -o mtxui -g mtxui -m 0750 /var/lib/mtxui
install -d -o root -g mtxui -m 0750 /etc/mtxui
install -d -o root -g root -m 0755 /usr/libexec/mtxui /usr/lib/systemd/system /usr/share/mtxui
install -o root -g root -m 0755 "$tmp/usr/libexec/mtxui/mtxui" /usr/libexec/mtxui/mtxui.new
install -o root -g root -m 0755 "$tmp/usr/libexec/mtxui/mediamtx" /usr/libexec/mtxui/mediamtx.new
install -d -o root -g root -m 0755 /usr/local/bin
install -o root -g root -m 0755 "$tmp/usr/local/bin/mtxui" /usr/local/bin/mtxui.new
mv -f /usr/libexec/mtxui/mtxui.new /usr/libexec/mtxui/mtxui
mv -f /usr/libexec/mtxui/mediamtx.new /usr/libexec/mtxui/mediamtx
mv -f /usr/local/bin/mtxui.new /usr/local/bin/mtxui
install -o root -g root -m 0644 "$tmp/usr/lib/systemd/system/mtxui.service" /usr/lib/systemd/system/mtxui.service
install -o root -g root -m 0644 "$tmp/usr/lib/systemd/system/mtxui-mediamtx.service" /usr/lib/systemd/system/mtxui-mediamtx.service
install -o root -g root -m 0644 "$tmp/usr/lib/systemd/system/mtxui-agent.service" /usr/lib/systemd/system/mtxui-agent.service
install -o root -g root -m 0644 "$tmp/usr/share/mtxui/mtxui.env.example" /usr/share/mtxui/mtxui.env.example
install -o root -g root -m 0644 "$tmp/usr/share/mtxui/mtxui-agent.env.example" /usr/share/mtxui/mtxui-agent.env.example
if [[ ! -e /etc/mtxui/mtxui.env ]]; then
  install -o root -g mtxui -m 0640 /usr/share/mtxui/mtxui.env.example /etc/mtxui/mtxui.env
else
  chown root:mtxui /etc/mtxui/mtxui.env
  chmod 0640 /etc/mtxui/mtxui.env
fi
systemctl daemon-reload
systemctl enable mtxui.service mtxui-mediamtx.service
for service in mtxui.service mtxui-mediamtx.service; do
  if systemctl is-active --quiet "$service"; then
    systemctl restart "$service"
  else
    systemctl start "$service"
  fi
done

cat <<'EOF'
MediaMTX UI and MediaMTX are installed as unprivileged systemd services.

The dashboard is bound to loopback until configured. Review /etc/mtxui/mtxui.env,
set MTXUI_PUBLIC_URL and MTXUI_PUBLIC_HOST to the addresses your users will reach,
and configure a trusted HTTPS reverse proxy before permitting remote sign-in.
Then apply settings with: systemctl restart mtxui.service mtxui-mediamtx.service

Read the first-run setup token with:
  sudo -u mtxui env MTXUI_DATA_DIR=/var/lib/mtxui /usr/libexec/mtxui/mtxui setup-token
Service logs: journalctl -u mtxui.service -u mtxui-mediamtx.service
EOF
