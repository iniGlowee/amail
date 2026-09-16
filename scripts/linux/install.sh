#!/usr/bin/env sh
# Install the amail binary and a systemd unit for one user.
# Usage: sudo scripts/linux/install.sh <user> [path/to/amail-binary]
# The user must already have run "amail init" and "amail join" (or do it after).
set -eu
USER_NAME="${1:?usage: install.sh <user> [binary]}"
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${2:-$HERE/../../dist/amail}"
if [ ! -f "$BIN" ]; then
  # Fall back to the newest linux build in dist/.
  BIN="$(ls -t "$HERE"/../../dist/amail-*-linux-"$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" 2>/dev/null | head -n1 || true)"
fi
[ -f "$BIN" ] || { echo "binary not found; pass it as the second argument" >&2; exit 1; }
id "$USER_NAME" >/dev/null 2>&1 || { echo "no such user: $USER_NAME" >&2; exit 1; }

install -m 755 "$BIN" /usr/local/bin/amail
install -m 644 "$HERE/amail@.service" /etc/systemd/system/amail@.service
systemctl daemon-reload
systemctl enable --now "amail@${USER_NAME}"
sleep 1
systemctl --no-pager --lines=5 status "amail@${USER_NAME}" || true
echo
echo "Installed. Logs: journalctl -u amail@${USER_NAME} -f   and   /home/${USER_NAME}/.amail/amail.log"
echo "Remember to open TCP 4444 inbound if this node should be reachable."
