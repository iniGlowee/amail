#!/usr/bin/env sh
# Install the amail binary and a systemd unit for one user.
#
#   sudo scripts/linux/install.sh <user> [path/to/amail-binary]
#
# Recommended: a dedicated, sudo-less account, e.g. "amail". If the user does
# not exist it is created as a system account with home /home/<user> and no
# login shell, and the person running sudo is added to its group so they can
# read and drop files in /home/<user>/AMail.
set -eu
USER_NAME="${1:?usage: install.sh <user> [binary]}"
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${2:-$HERE/../../dist/amail}"
if [ ! -f "$BIN" ]; then
  BIN="$(ls -t "$HERE"/../../dist/amail-*-linux-"$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" 2>/dev/null | head -n1 || true)"
fi
[ -f "$BIN" ] || { echo "binary not found; pass it as the second argument" >&2; exit 1; }

if ! id "$USER_NAME" >/dev/null 2>&1; then
  useradd --system --create-home --home-dir "/home/$USER_NAME" --shell /sbin/nologin "$USER_NAME"
  echo "created system user $USER_NAME"
fi
HOME_DIR="$(getent passwd "$USER_NAME" | cut -d: -f6)"
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "$USER_NAME" ]; then
  usermod -aG "$USER_NAME" "$SUDO_USER"
  echo "added $SUDO_USER to group $USER_NAME (takes effect at next login)"
fi
chmod 750 "$HOME_DIR"
mkdir -p "$HOME_DIR/AMail"
chown "$USER_NAME:$USER_NAME" "$HOME_DIR/AMail"
chmod 2775 "$HOME_DIR/AMail"

install -m 755 "$BIN" /usr/local/bin/amail
install -m 644 "$HERE/amail@.service" /etc/systemd/system/amail@.service
systemctl daemon-reload

if [ ! -f "$HOME_DIR/.amail/config.json" ]; then
  echo
  echo "No node config yet. Initialise it as $USER_NAME, then start the service:"
  echo "  sudo -u $USER_NAME -H amail init --id $(hostname -s | tr 'A-Z' 'a-z') --mailbox $HOME_DIR/AMail"
  echo "  sudo -u $USER_NAME -H amail join /path/to/<id>.amailkey"
  echo "  sudo -u $USER_NAME -H amail whitelist add <id> [host]"
  echo "  sudo systemctl enable --now amail@$USER_NAME"
  exit 0
fi
systemctl enable --now "amail@${USER_NAME}"
sleep 1
systemctl --no-pager --lines=5 status "amail@${USER_NAME}" || true
echo
echo "Installed. Logs: journalctl -u amail@${USER_NAME} -f   and   $HOME_DIR/.amail/amail.log"
echo "Remember to open TCP 4444 inbound if this node should be reachable."
