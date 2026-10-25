---
name: pp-kroger
description: "Printing Press CLI for Kroger. Kroger grocery CLI — search products, read order history, and add staples to your cart using your logged-in session."
author: "bschm0622"
license: "Apache-2.0"
argument-hint: "<command> [args] | install cli|mcp"
allowed-tools: "Read Bash"
metadata:
  openclaw:
    requires:
      bins:
        - kroger-pp-cli
    install:
      - kind: go
        bins: [kroger-pp-cli]
        module: github.com/bschm0622/kroger-cart/cmd/kroger-pp-cli
---

# Kroger — Printing Press CLI

## Prerequisites: Install the CLI

This skill drives the `kroger-pp-cli` binary. **You must verify the CLI is installed before invoking any command from this skill.** If it is missing, install it first:

1. Install with Go (1.26.4 or newer). This installs into `$GOPATH/bin` (default `$HOME/go/bin`):
   ```bash
   go install github.com/bschm0622/kroger-cart/cmd/kroger-pp-cli@latest
   ```
2. Verify: `kroger-pp-cli --version`
3. Ensure that directory is on `$PATH` for the agent/runtime that will invoke this skill.

If `--version` reports "command not found" after install, the runtime cannot see the binary directory on `$PATH`. Do not proceed with skill commands until verification succeeds.

Kroger grocery CLI — search products, read order history, and add staples to your cart using your logged-in session.

## HTTP Transport

This CLI uses Chrome-compatible HTTP transport for browser-facing endpoints. It does not require a resident browser process for normal API calls.

## Command Reference

**cart** — Add items to your Kroger cart

- `kroger-pp-cli cart add` — Add/update line items in your cart. Pipe the JSON body via --stdin (shape: {'lineItems':[{'gtin13':...
- `kroger-pp-cli cart savings` — Compute savings for a set of line items. Pipe JSON body via --stdin.

**customer_profile_profile** — Your Kroger account profile (also an auth check)

- `kroger-pp-cli customer-profile-profile` — Get your customer profile (use to verify the session is authenticated)

**orders** — Past purchases / order history

- `kroger-pp-cli orders details` — Get line items for past transactions. Pipe a JSON array via --stdin: [{'divisionNumber':..,'storeNumber':..
- `kroger-pp-cli orders history` — List past purchase transactions (most recent first)

**products** — Search and look up Kroger products

- `kroger-pp-cli products get` — Get product details for one or more gtin13s (comma-separated)
- `kroger-pp-cli products search` — Search products by keyword (returns items with UPC/gtin13, price, availability)

**usual** — Kroger "your usual items" recommendations

- `kroger-pp-cli usual` — Get usual-product recommendations. Pipe JSON via --stdin: {'carts':[{'id':'<cart_id>','upcs':[..]}]}.


### Finding the right command

When you know what you want to do but not which command does it, ask the CLI directly:

```bash
kroger-pp-cli which "<capability in your own words>"
```

`which` resolves a natural-language capability query to the best matching command from this CLI's curated feature index. Exit code `0` means at least one match; exit code `2` means no confident match — fall back to `--help` or use a narrower query.

## Auth Setup

This CLI uses a browser session. Log in to .kroger.com in Chrome, then:

```bash
kroger-pp-cli auth login --chrome
```

Requires a cookie extraction tool (`pycookiecheat` via pip, or `cookies` via Homebrew).

Run `kroger-pp-cli doctor` to verify setup.

## Agent Mode

Add `--agent` to any command. Expands to: `--json --compact --no-input --no-color --yes`.

- **Pipeable** — JSON on stdout, errors on stderr
- **Filterable** — `--select` keeps a subset of fields. Dotted paths descend into nested structures; arrays traverse element-wise. Critical for keeping context small on verbose APIs:

  ```bash
  kroger-pp-cli customer-profile-profile --agent --select id,name,status
  ```
- **Previewable** — `--dry-run` shows the request without sending
- **Offline-friendly** — sync/search commands can use the local SQLite store when available
- **Non-interactive** — never prompts, every input is a flag
- **Explicit retries** — use `--idempotent` only when an already-existing create should count as success

### Response envelope

Commands that read from the local store or the API wrap output in a provenance envelope:

```json
{
  "meta": {"source": "live" | "local", "synced_at": "...", "reason": "..."},
  "results": <data>
}
```

Parse `.results` for data and `.meta.source` to know whether it's live or local. A human-readable `N results (live)` summary is printed to stderr only when stdout is a terminal AND no machine-format flag (`--json`, `--csv`, `--compact`, `--quiet`, `--plain`, `--select`) is set — piped/agent consumers and explicit-format runs get pure JSON on stdout.

## Agent Feedback

When you (or the agent) notice something off about this CLI, record it:

```
kroger-pp-cli feedback "the --since flag is inclusive but docs say exclusive"
kroger-pp-cli feedback --stdin < notes.txt
kroger-pp-cli feedback list --json --limit 10
```

Entries are stored locally at `~/.local/share/kroger-pp-cli/feedback.jsonl`. They are never POSTed unless `KROGER_FEEDBACK_ENDPOINT` is set AND either `--send` is passed or `KROGER_FEEDBACK_AUTO_SEND=true`. Default behavior is local-only.

Write what *surprised* you, not a bug report. Short, specific, one line: that is the part that compounds.

## Output Delivery

Every command accepts `--deliver <sink>`. The output goes to the named sink in addition to (or instead of) stdout, so agents can route command results without hand-piping. Three sinks are supported:

| Sink | Effect |
|------|--------|
| `stdout` | Default; write to stdout only |
| `file:<path>` | Atomically write output to `<path>` (tmp + rename) |
| `webhook:<url>` | POST the output body to the URL (`application/json` or `application/x-ndjson` when `--compact`) |

Unknown schemes are refused with a structured error naming the supported set. Webhook failures return non-zero and log the URL + HTTP status on stderr.

## Named Profiles

A profile is a saved set of flag values, reused across invocations. Use it when a scheduled agent calls the same command every run with the same configuration - HeyGen's "Beacon" pattern.

```
kroger-pp-cli profile save briefing --json
kroger-pp-cli --profile briefing customer-profile-profile
kroger-pp-cli profile list --json
kroger-pp-cli profile show briefing
kroger-pp-cli profile delete briefing --yes
```

Explicit flags always win over profile values; profile values win over defaults. `agent-context` lists all available profiles under `available_profiles` so introspecting agents discover them at runtime.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 2 | Usage error (wrong arguments) |
| 3 | Resource not found |
| 4 | Authentication required |
| 5 | API error (upstream issue) |
| 7 | Rate limited (wait and retry) |
| 10 | Config error |

## Argument Parsing

Parse `$ARGUMENTS`:

1. **Empty, `help`, or `--help`** → show `kroger-pp-cli --help` output
2. **Starts with `install`** → ends with `mcp` → MCP installation; otherwise → see Prerequisites above
3. **Anything else** → Direct Use (execute as CLI command with `--agent`)

## MCP Server Installation

1. Install the MCP server:
   ```bash
   go install github.com/bschm0622/kroger-cart/cmd/kroger-pp-mcp@latest
   ```
2. Register with Claude Code:
   ```bash
   claude mcp add kroger-pp-mcp -- kroger-pp-mcp
   ```
3. Verify: `claude mcp list`

## Direct Use

1. Check if installed: `which kroger-pp-cli`
   If not found, offer to install (see Prerequisites at the top of this skill).
2. Match the user query to the best command from the Unique Capabilities and Command Reference above.
3. Execute with the `--agent` flag:
   ```bash
   kroger-pp-cli <command> [subcommand] [args] --agent
   ```
4. If ambiguous, drill into subcommand help: `kroger-pp-cli <command> --help`.
