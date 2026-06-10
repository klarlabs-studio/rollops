#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${PREFIX:-/usr/local}"
SYSCONFDIR="${SYSCONFDIR:-/etc/rollops}"
UNITDIR="${UNITDIR:-/etc/systemd/system}"
STATE_DIR="${STATE_DIR:-/var/lib/rollops}"
USER_NAME="${ROLLOPS_USER:-rollops}"
GROUP_NAME="${ROLLOPS_GROUP:-$USER_NAME}"

usage() {
  cat <<EOF
Usage: scripts/install-systemd.sh [--no-build] [--no-enable]

Installs Rollops binaries, env template, and systemd unit.

Environment overrides:
  PREFIX       binary prefix (default: /usr/local)
  SYSCONFDIR   env directory (default: /etc/rollops)
  UNITDIR      systemd unit directory (default: /etc/systemd/system)
  STATE_DIR    runtime state dir (default: /var/lib/rollops)
  ROLLOPS_USER service user (default: rollops)
  ROLLOPS_GROUP service group (default: same as ROLLOPS_USER)
EOF
}

build=1
enable=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-build) build=0 ;;
    --no-enable) enable=0 ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if [ "$(id -u)" -ne 0 ]; then
  echo "install-systemd.sh must run as root" >&2
  exit 1
fi

if [ "$build" -eq 1 ]; then
  make -C "$ROOT_DIR" build
fi

if ! getent group "$GROUP_NAME" >/dev/null; then
  groupadd --system "$GROUP_NAME"
fi
if ! id -u "$USER_NAME" >/dev/null 2>&1; then
  useradd --system --gid "$GROUP_NAME" --home-dir "$STATE_DIR" --shell /usr/sbin/nologin "$USER_NAME"
fi

install -d -m 0755 "$PREFIX/bin"
install -m 0755 "$ROOT_DIR/bin/rollops" "$PREFIX/bin/rollops"
install -m 0755 "$ROOT_DIR/bin/rollopsd" "$PREFIX/bin/rollopsd"

install -d -o root -g "$GROUP_NAME" -m 0750 "$SYSCONFDIR"
if [ ! -f "$SYSCONFDIR/rollopsd.env" ]; then
  install -o root -g "$GROUP_NAME" -m 0640 "$ROOT_DIR/deploy/systemd/rollopsd.env.example" "$SYSCONFDIR/rollopsd.env"
  echo "created $SYSCONFDIR/rollopsd.env; edit tokens/passwords before starting"
else
  echo "kept existing $SYSCONFDIR/rollopsd.env"
fi

install -d -o "$USER_NAME" -g "$GROUP_NAME" -m 0750 "$STATE_DIR"
install -m 0644 "$ROOT_DIR/deploy/systemd/rollopsd.service" "$UNITDIR/rollopsd.service"

systemctl daemon-reload
if [ "$enable" -eq 1 ]; then
  systemctl enable rollopsd.service
fi

cat <<EOF
Installed Rollops systemd unit.

Next:
  1. Edit $SYSCONFDIR/rollopsd.env
  2. sudo systemctl start rollopsd
  3. sudo systemctl status rollopsd
  4. ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=<token> rollops doctor
EOF
