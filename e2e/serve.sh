#!/usr/bin/env bash
# Starts the real page against the fake Kroger, with a throwaway config and
# staples file, for the end-to-end tests. Never touches ~/.config.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/e2e/.tmp"
[ -d "$DIR" ] && chmod -R u+w "$DIR"
rm -rf "$DIR"
mkdir -p "$DIR"

cat >"$DIR/config.toml" <<'EOF'
base_url = "http://127.0.0.1:18788"
access_token = "bm_sz=test; _abck=test"

[headers]
x-modality = '{"type":"PICKUP","locationId":"00000"}'
x-laf-object = '[{"modality":{"type":"PICKUP","handoffAddress":{"address":{"name":"Test Kroger"}}}}]'
EOF

# Milk is "your usual"; the rest are staples. 104 is one Kroger no longer has.
printf '%s\n' \
	$'1\t0000000000101\t12x\tTest Whole Milk' \
	$'# 1\t0000000000102\t9x\tTest Bananas' \
	$'# 1\t0000000000103\t7x\tTest Olive Oil' \
	$'# 1\t0000000000104\t5x\tTest Discontinued Salsa' \
	$'# 1\t0000000000105\t4x\tTest Sourdough Bread' \
	>"$DIR/staples.tsv"

cd "$ROOT"
go build -o "$DIR/kroger-pp-cli" ./cmd/kroger-pp-cli
mkdir -p "$DIR/home"
HOME="$DIR/home" exec "$DIR/kroger-pp-cli" serve --config "$DIR/config.toml" --addr 127.0.0.1:18787 --no-open
