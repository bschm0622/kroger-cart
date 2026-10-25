# Authenticating the Kroger CLI

The CLI talks to kroger.com's private API using **your logged-in session cookies**.

For the weekly grocery order this exists to serve, run `kroger-pp-cli serve`
(or double-click the `scripts/Kroger Cart.command` launcher). The page asks for a fresh
session itself when it needs one.

Kroger's auth cookies are httpOnly, which puts them out of reach of anything
running inside the page — but they *are* on disk, in Chrome's cookie database,
encrypted with a key in your login keychain. So the fast path is to read them
from there:

```bash
./kroger-pp-cli auth from-chrome
```

See [Reading the session from Chrome](#reading-the-session-from-chrome) below.
The manual DevTools copy is documented first because it works on any browser and
is what to fall back to.

The `press-auth` companion, on the other hand, genuinely doesn't work: Kroger fronts
`login.kroger.com` with Akamai, which fingerprints press-auth's controlled Chrome
window and serves an **Access Denied** interstitial before the sign-in form even
loads:

```
Access Denied
You don't have permission to access
"http://login.kroger.com/eciamp.onmicrosoft.com/b2c_1a__ciam_signin_signup/..."
Reference #18.52423917...
```

So the working path is to copy the session out of your **normal** Chrome — the one
Akamai already trusts — and paste it into the CLI.

## One-time setup

Do this **in your terminal and your own browser**; your credentials never pass
through Claude.

1. In your normal Chrome, sign in to <https://www.kroger.com> as usual.
2. Open **DevTools** (⌥⌘I) → **Network** tab, filter on `atlas`.
3. Find a request to `customer-profile/v1/profile` **whose Status column reads
   200**. This check matters: a session can be present and still unauthorized,
   and the copied cookies then authenticate (no 401) while every account-scoped
   call returns `403 FORBIDDEN_ACTION`. Copying from a request you have watched
   succeed is the difference between a working jar and an hour of debugging.
4. In **Headers → Request Headers**, right-click the `Cookie:` entry → **Copy
   value**. It'll be long (several KB) — that's expected.
5. Hand it to the CLI, from the top of this repo:

```bash
pbpaste | ./kroger-pp-cli auth set-cookie -
```

`-` reads from stdin, which keeps a multi-kilobyte session out of your shell
history and out of the process argument list. You should see
`OK Saved <n> cookies to …`.

Never paste the cookie as a command-line argument: it contains `;` and `&`,
which zsh treats as metacharacters, so it will not reach the binary intact.

## Reading the session from Chrome

On macOS, skip all of that:

```bash
./kroger-pp-cli auth from-chrome        # OK Read 45 cookies from Chrome profile "Default"
./kroger-pp-cli doctor                  # want: OK Auth
```

The web UI has the same thing as a button — **Import from Chrome**, on the panel
that asks for a session.

Chrome keeps its cookies in a SQLite database under `~/Library/Application
Support/Google/Chrome/<profile>/Cookies`, with each value encrypted
AES-128-CBC under a key derived (PBKDF2-HMAC-SHA1, salt `saltysalt`, 1003
rounds) from the **Chrome Safe Storage** item in your login keychain. This reads
that database and decrypts the `kroger.com` cookies. It never writes anything,
and it uses the `security(1)` binary to fetch the key so macOS shows you its own
permission dialog — click **Allow** the first time.

Details worth knowing:

- Only `.kroger.com` and `www.kroger.com` cookies at path `/` are taken. The
  `login.kroger.com` jar is deliberately excluded: it isn't sent to
  `www.kroger.com`, and including it has produced 403s.
- This picks up the httpOnly Akamai cookies (`_abck`, `bm_sz`, …) that DevTools
  shows but page scripts can't read, which is why the resulting jar works.
- With several Chrome profiles it picks the one whose Kroger cookies were
  updated most recently — the one you were just browsing in. `--profile
  "Profile 1"` overrides.
- The database is copied to a temp file before being opened read-only, because
  Chrome holds the live file open. The copy is deleted immediately; it holds
  decryptable session cookies.
- macOS and Chrome only. Other browsers still need the DevTools copy above.

### Store context

The cookies alone are enough for `customer-profile-profile` and `orders
history`. Product search additionally needs the store-context headers, or it
returns `400 LAF_OBJECTS_REQUIRED`. Add them to `~/.config/kroger-pp-cli/config.toml`,
copied from any browser request's headers:

```toml
[headers]
"x-laf-object" = "..."
"x-modality" = "..."
"x-geo-location-v1" = "..."
"x-facility-id" = "..."      # your store's number, if requests include it
```

These describe which store you're shopping, not who you are, so they change
only when you switch stores.

## Verify it worked

```bash
./kroger-pp-cli doctor                              # want: OK Auth
./kroger-pp-cli customer-profile-profile --agent | head
```

- **Success:** JSON with your profile (a `customerProfile`/`data` object).
- **`401 AUTH_REQUIRED`:** the paste was incomplete, or the session already
  expired. Re-copy the header.
- **`403 FORBIDDEN_ACTION`:** the cookies authenticate but the session was not
  authorized. This is not a bot block — the response carries `Server: nginx`
  and a `Server-Timing: origin` entry, so Akamai forwarded it and Kroger's
  origin refused. Re-copy from a request you have watched return 200 (step 3).

## What the CLI cannot do

`orders details` (`purchase-history/v2/details`) is the one endpoint of the 45
captured that Akamai Bot Manager protects. The browser sends an
`akamai-bm-telemetry` header carrying a sensor payload built by Akamai's
obfuscated JS; it is bound to a live JS runtime and cannot be replayed from a
captured session. Akamai resets the HTTP/2 stream before Kroger's origin sees
the request, so there is no status code to interpret. The command detects this
and says so. Use `orders history`, which is not protected, for
transaction-level data.

To see what's being sent without sending it: `--dry-run` prints the request,
including a masked `Cookie:` line.

## When the session expires

These are session cookies, so expect this in days, not months —
`customer-profile-profile` starts returning `401`. Run `auth from-chrome` again
(or repeat the copy-paste) — as long as you're still signed in in Chrome, Chrome
has already refreshed them for you.

There's no unattended refresh. Adding one means identifying which cookie carries
Kroger's JWT and teaching press-auth to renew it (`--jwt-carrier-cookie <name>`
plus `--refresh-endpoint`), which is only worth doing if press-auth can get past
Akamai in the first place.

## When something breaks

| Symptom | Cause | Fix |
| --- | --- | --- |
| `401 AUTH_REQUIRED` | session expired | `kroger-pp-cli auth from-chrome`, or **Import from Chrome** on the page |
| `403 FORBIDDEN_ACTION` | cookies copied from a request that wasn't itself a 200 | recopy from a request you watched return 200 |
| `400 LAF_OBJECTS_REQUIRED` | store headers missing from `config.toml` | copy `x-laf-object`, `x-modality`, `x-geo-location-v1` from any `atlas` request |
| `429` / "Kroger is limiting requests" | Akamai rate limit | wait a minute |
| `no ACTIVE cart` | account has no cart yet | add one item on kroger.com |
| `orders details` fails | Akamai Bot Manager; unfixable from a CLI | use `orders history` |
| every command fails with `INTERNAL_ERROR` / `stream error` | the whole cookie jar aged out | refresh the session; it is not a permanent block |
| `serve` says `address already in use` | a copy is already running | reuse that tab, or `--addr 127.0.0.1:9000` |

## Note on the CLI's auth plumbing

As generated, the CLI never sent a credential on *any* request: `client.New`
passed a nil cookie jar, and the branch meant to attach cookies was an empty
comment block deferring to a `LoadCookieJar` function the generator never emitted.
Every authenticated call would have returned `401` no matter how the session was
captured. Fixed by hand after generation, with regression tests in
`internal/client/cookie_auth_test.go` — the failure mode was silent, so it needs
them.
