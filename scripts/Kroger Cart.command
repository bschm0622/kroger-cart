#!/bin/bash
# Double-clickable launcher for the Kroger cart UI.
#
# Finder runs a .command file by opening Terminal in it, which is what keeps
# the server alive: closing the window stops it. Put a copy (or an alias) on
# the Desktop — it locates the repo from its own path, so it works from
# anywhere as long as this file stays inside the checkout.
#
# If a server is already up on the port, this opens the existing one rather
# than failing on a bind conflict.
set -uo pipefail

ADDR="${KROGER_ADDR:-127.0.0.1:8787}"
URL="http://$ADDR/"
REPO="$(cd "$(dirname "$(readlink "${BASH_SOURCE[0]}" || echo "${BASH_SOURCE[0]}")")/.." && pwd)"
BIN="$REPO/kroger-pp-cli"

cd "$REPO" || { echo "Could not find the checkout at $REPO"; read -r -p "Press return to close."; exit 1; }

if curl -sf -o /dev/null --max-time 2 "$URL"; then
	echo "Already running — opening $URL"
	open "$URL"
	exit 0
fi

# Build on first run, or after a git pull, so the launcher is never pointing
# at a binary older than the code. go build is a no-op when nothing changed.
if ! command -v go >/dev/null 2>&1; then
	if [ ! -x "$BIN" ]; then
		echo "Go is not installed and there is no built binary at $BIN."
		read -r -p "Press return to close."
		exit 1
	fi
else
	echo "Building..."
	if ! go build -o "$BIN" ./cmd/kroger-pp-cli; then
		echo
		echo "Build failed. The last working binary is still at $BIN."
		[ -x "$BIN" ] || { read -r -p "Press return to close."; exit 1; }
	fi
fi

echo "Starting the Kroger cart UI at $URL"
echo "Close this window (or press Ctrl-C) when you're done."
echo
exec "$BIN" serve --addr "$ADDR"
