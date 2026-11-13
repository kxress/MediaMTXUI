#!/usr/bin/env bash
set -euo pipefail

repo=${MTXUI_REPOSITORY:-greatmastix/MediaMTXUI}
version=latest
while (($#)); do
  case $1 in
    --version)
      (($# >= 2)) || { echo "install-native-agent.sh: --version needs a release tag" >&2; exit 2; }
      version=$2
      shift 2
      ;;
    --repository)
      (($# >= 2)) || { echo "install-native-agent.sh: --repository needs owner/repository" >&2; exit 2; }
      repo=$2
      shift 2
      ;;
    -h|--help)
      echo "Usage: install-native-agent.sh [--version vX.Y.Z] [--repository owner/repository]"
      echo "Use --repository owner/repository to select a fork's releases."
      exit 0
      ;;
    *) echo "install-native-agent.sh: unknown option: $1" >&2; exit 2 ;;
  esac
done

[[ $(id -u) == 0 ]] || { echo "Run this installer as root (for example, with sudo)." >&2; exit 1; }
[[ $(uname -s) == Linux ]] || { echo "The native agent supports Linux only." >&2; exit 1; }
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
grep -q '^usr/lib/systemd/system/mtxui-agent.service$' "$tmp/archive.list"
grep -q '^usr/share/mtxui/mtxui-agent.env.example$' "$tmp/archive.list"
tar -xzf "$tmp/$asset" -C "$tmp"

if ! getent group mtxui >/dev/null; then groupadd --system mtxui; fi
if ! id mtxui >/dev/null 2>&1; then
  useradd --system --gid mtxui --home-dir /var/lib/mtxui-agent --create-home --shell /usr/sbin/nologin mtxui
fi
install -d -o mtxui -g mtxui -m 0750 /var/lib/mtxui-agent /var/lib/mtxui-agent/recordings
install -d -o root -g mtxui -m 0750 /etc/mtxui /usr/libexec/mtxui /usr/lib/systemd/system /usr/share/mtxui
install -o root -g root -m 0755 "$tmp/usr/libexec/mtxui/mtxui" /usr/libexec/mtxui/mtxui.new
mv -f /usr/libexec/mtxui/mtxui.new /usr/libexec/mtxui/mtxui
install -o root -g root -m 0644 "$tmp/usr/lib/systemd/system/mtxui-agent.service" /usr/lib/systemd/system/mtxui-agent.service
install -o root -g root -m 0644 "$tmp/usr/share/mtxui/mtxui-agent.env.example" /usr/share/mtxui/mtxui-agent.env.example
if [[ ! -e /etc/mtxui/agent.env ]]; then
  install -o root -g mtxui -m 0640 /usr/share/mtxui/mtxui-agent.env.example /etc/mtxui/agent.env
else
  chown root:mtxui /etc/mtxui/agent.env
  chmod 0640 /etc/mtxui/agent.env
fi
systemctl daemon-reload

cat <<'EOF'
The MediaMTX UI agent files are installed. It is not enabled or started until mTLS certificates,
the callback URL and key, the MediaMTX config path, and filesystem permissions are configured.

Edit /etc/mtxui/agent.env, then configure MediaMTX authHTTPAddress as
http://127.0.0.1:9081/internal/auth and bind apiAddress, metricsAddress and playbackAddress to loopback.
Give the mtxui service account access only to the configured MediaMTX YAML file and validator binary.
For a config file outside /var/lib/mtxui-agent, add its directory to the service's ReadWritePaths.

Then run: systemctl enable --now mtxui-agent.service
Logs: journalctl -u mtxui-agent.service
EOF
