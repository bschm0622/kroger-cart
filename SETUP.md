# Setting up Kroger Cart (instructions for an AI agent)

You're helping someone set up Kroger Cart on their Mac: a local page for
filling their kroger.com cart from what they actually buy. Work through the
steps in order. After each one, run its check before moving on, and tell the
person in a sentence what just happened.

Some steps need the person: signing in to kroger.com, allowing a macOS
keychain prompt, and copying one request out of Chrome. Ask for those plainly,
one at a time, and wait for them.

## Rules

- **Keep secrets out of the chat.** The Kroger session and the copied request
  contain login cookies. Never ask the person to paste them into the
  conversation; have them copy to the clipboard and pipe it in with `pbpaste`,
  as shown below. Never print or `cat` `~/.config/kroger-pp-cli/config.toml`.
- **Don't change their cart during setup.** Every check below only reads. Don't
  run `cart add`, `scripts/kroger-weekly-cart` without `--dry-run`, or anything
  that sends to `/api/cart/update`. Nothing in this project checks out.
- **Ask before installing anything** (Go, Homebrew) or writing outside this
  folder (the Desktop shortcut in step 5).
- Run every command from the top of this repository.

## 0. Check the machine

```bash
sw_vers -productName      # want: macOS
go version                # want: go1.26.4 or newer
python3 --version         # used by the staples script
ls -d "/Applications/Google Chrome.app"
```

- Not macOS, or no Chrome: setup still works, but sign-in is by hand. Follow
  [AUTH-SETUP.md](AUTH-SETUP.md) for step 2 instead.
- Go missing or older than 1.26.4: offer to install it (`brew install go`, or
  the installer from https://go.dev/dl/), and wait for a yes.
- No `python3`: `xcode-select --install` provides it. Ask first.

## 1. Build

```bash
go build -o kroger-pp-cli ./cmd/kroger-pp-cli
./kroger-pp-cli version
```

Check: `version` prints a version number.

## 2. Sign in

Ask the person to open kroger.com in Chrome, sign in, and choose the store
they use for pickup or delivery if the site asks. When they say they're signed
in:

```bash
./kroger-pp-cli auth from-chrome
```

macOS shows a keychain prompt asking to use "Chrome Safe Storage". Tell them
to expect it and click **Always Allow**; otherwise the next sign-in asks
again. If they use several Chrome profiles, it picks the one that used Kroger
most recently; `--profile "Profile 1"` picks another.

Check:

```bash
./kroger-pp-cli doctor        # want a line reading OK Auth
```

## 3. Set the store

Product search needs to know which store they shop at, and kroger.com only
sends that inside its own requests. Ask the person to do this, word for word:

1. On kroger.com in Chrome, search for anything, for example "milk".
2. Press ⌥⌘I to open DevTools, and click the **Network** tab.
3. Type `products-search` in the filter box. If the list is empty, search
   again with DevTools open.
4. Right-click the `products-search` row → **Copy** → **Copy as cURL**.
5. Tell you when it's copied. They don't need to paste it anywhere.

Then:

```bash
pbpaste | ./kroger-pp-cli auth set-store -
```

It keeps only the store headers and ignores everything else in the paste,
cookies included. If it says there's no `x-laf-object`, the wrong request was
copied; repeat from step 3 of their list.

Check (read-only):

```bash
./kroger-pp-cli products search --keyword milk --agent | head -c 300
```

Want: JSON containing products. A `400 LAF_OBJECTS_REQUIRED` means the store
didn't save; redo this step.

## 4. Build their staples list

```bash
./scripts/kroger-staples
```

This reads their last 100 trips and writes `~/.config/kroger-pp-cli/staples.tsv`
with everything bought on two or more trips. It doesn't touch the cart.

Check: it prints `Resolving names for N items...` and then `Wrote <path>`.
"No item was bought 2+ times" means the account has little order history; the
page still works, using Search. If a staples file already exists, it writes
`staples.tsv.new` beside it instead of overwriting; ask before using `--force`.

## 5. Open the page, and add a shortcut

Ask whether they'd like a **Kroger Cart** shortcut on the Desktop. If yes:

```bash
ln -s "$PWD/scripts/Kroger Cart.command" "$HOME/Desktop/Kroger Cart.command"
```

It has to be a link, not a copy: the launcher finds the app from where the
original file lives.

Then start it:

```bash
./kroger-pp-cli serve
```

This opens <http://127.0.0.1:8787> and keeps running until stopped. If you
can't leave a process running, tell them to double-click the shortcut instead.

Check: `curl -sf -o /dev/null http://127.0.0.1:8787/ && echo up`

## Done: what to tell them

- **Each week:** double-click **Kroger Cart**. Add what you want from Staples,
  Search, Recipes or Past trips; the right-hand panel shows your Kroger cart.
  Press **Update Kroger cart** to send changes, then check out on kroger.com.
- **Your usual:** once the cart holds a normal week's shop, **Save as usual**
  remembers it, and **Add your usual** brings the cart back up to it.
- **When the page says the session expired** (every few days), press
  **Import from Chrome** on the page, while signed in to kroger.com in Chrome.
- **If they switch stores**, repeat step 3.

## If something fails

The table at the end of [AUTH-SETUP.md](AUTH-SETUP.md) covers the common
errors (401, 403, 429, `LAF_OBJECTS_REQUIRED`, address already in use). Kroger
limits bursts of requests; on a 429, wait a minute rather than retrying in a
loop.
