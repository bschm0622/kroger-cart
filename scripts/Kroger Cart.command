#!/bin/bash
set -uo pipefail

ADDR="${KROGER_ADDR:-127.0.0.1:8787}"
URL="http://$ADDR/"
HERE="$(python3 -c 'import os, sys; print(os.path.dirname(os.path.realpath(sys.argv[1])))' "${BASH_SOURCE[0]}")"
REPO="$(cd "$HERE/.." && pwd)"
BIN="$REPO/kroger-pp-cli"

cd "$REPO" || { echo "Could not find the checkout at $REPO"; read -r -p "Press return to close."; exit 1; }

if curl -sf -o /dev/null --max-time 2 "$URL"; then
	echo "Already running — opening $URL"
	open "$URL"
	exit 0
fi

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
