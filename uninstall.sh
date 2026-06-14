#!/bin/sh
# tunneldir uninstaller.
#
#   curl -fsSL https://raw.githubusercontent.com/tallbiscuits/tunneldir/main/uninstall.sh | sh
#
# Removes the binary from ~/.local/bin (or $INSTALL_DIR). Config and runtime
# state are kept unless you confirm their removal (or pass --purge).
set -eu

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BIN="${INSTALL_DIR}/tunneldir"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/tunneldir"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/tunneldir"

purge=0
[ "${1:-}" = "--purge" ] && purge=1

# Tear down the systemd autostart unit (and stop tunnels) while the binary still
# exists; it knows where its own unit lives.
if [ -x "$BIN" ]; then
  "$BIN" uninstall --run >/dev/null 2>&1 || true
fi

if [ -e "$BIN" ]; then
  rm -f "$BIN"
  echo "removed $BIN"
else
  echo "no binary at $BIN"
fi

# Config + state are user data; only remove them on explicit confirmation.
if [ "$purge" -eq 0 ] && [ -r /dev/tty ] && { [ -d "$CONFIG_DIR" ] || [ -d "$STATE_DIR" ]; }; then
  printf "also remove config (%s) and state (%s)? [y/N] " "$CONFIG_DIR" "$STATE_DIR"
  read -r ans < /dev/tty || ans=""
  case "$ans" in y|Y|yes|YES) purge=1 ;; esac
fi

if [ "$purge" -eq 1 ]; then
  rm -rf "$CONFIG_DIR" "$STATE_DIR"
  echo "removed $CONFIG_DIR and $STATE_DIR"
else
  echo "kept config ($CONFIG_DIR) and state ($STATE_DIR); remove with --purge"
fi

echo "done."
