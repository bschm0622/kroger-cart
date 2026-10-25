# Signing in and setting your store

Kroger Cart talks to kroger.com as you, using the session your browser already
has. It needs two things: that session, and which store you shop at.

## 1. Sign in from Chrome

Sign in to <https://www.kroger.com> in Chrome, then:

```bash
./kroger-pp-cli auth from-chrome
./kroger-pp-cli doctor          # want: OK Auth
```

This reads the Kroger cookies from Chrome's own cookie store. macOS asks once
for permission to use the "Chrome Safe Storage" keychain item; choose **Always
Allow** so you aren't asked every week. It only reads, and only `kroger.com`
cookies.

- **Just signed in and it says you're not?** Chrome saves cookies to disk every
  few seconds, not instantly. Wait about 30 seconds and run it again.
- **Several Chrome profiles?** It picks the one that used kroger.com most
  recently. `--profile "Profile 1"` picks another.
- **The page does this for you.** When a session expires, the page first tries
  Chrome by itself, then shows **Import from Chrome** if that didn't work.

## 2. Set your store

Search, prices and stock depend on your store, and kroger.com only says which
store you use inside its own requests. So:

1. On kroger.com in Chrome, search for anything.
2. Open DevTools (⌥⌘I) → **Network**, and type `products-search` in the filter box.
3. Right-click that request → **Copy** → **Copy as cURL**.
4. Run:

```bash
pbpaste | ./kroger-pp-cli auth set-store -
```

Only the store headers are kept; the rest of the paste, cookies included, is
ignored. Redo this if you change stores or switch between pickup and delivery.
The page shows the store it's using in the top bar.

## 3. Check it worked

```bash
./kroger-pp-cli doctor                              # want: OK Auth
./kroger-pp-cli products search --keyword milk --agent | head -c 300
```

You should see products. `400 LAF_OBJECTS_REQUIRED` means the store isn't set.

## Not using Chrome?

Copy the session by hand instead:

1. On kroger.com, signed in, open DevTools (⌥⌘I) → **Network**, filter `atlas`.
2. Click a `customer-profile/v1/profile` request **whose Status reads 200**. A
   session copied from any other request can sign you in while every account
   call fails with 403.
3. **Headers → Request Headers** → right-click `Cookie:` → **Copy value**.
4. Run `pbpaste | ./kroger-pp-cli auth set-cookie -`

The page's sign-in box has a paste field that does the same thing. Don't paste
the cookie as a command-line argument: it contains `;` and `&`, which the shell
mangles.

## When the session expires

Kroger sessions last days, not months. Run `auth from-chrome` again, or press
**Import from Chrome** on the page. As long as you're signed in to kroger.com in
Chrome, Chrome has already refreshed the session.

## What doesn't work

- **Other Kroger-family sites.** Ralphs, Fred Meyer, King Soopers, Smith's,
  Fry's, QFC and the rest use their own websites; this only knows kroger.com.
- **Signing in from the command line.** Kroger's bot protection blocks
  automated browsers at the sign-in page, which is why this borrows Chrome's
  session instead.
- **`orders details`** (line items for a past order) is protected in a way a
  command-line tool can't get past. `orders history` works, and so does the
  page's Past trips tab.

## When something breaks

| Symptom | Cause | Fix |
| --- | --- | --- |
| "Your Kroger session has expired" / `401` | the session aged out | **Import from Chrome**, or `auth from-chrome` |
| `403` | session copied from a request that wasn't itself a 200 | copy again from a request you watched return 200, or use Chrome |
| `400 LAF_OBJECTS_REQUIRED` / "Set your store" | store not set | `auth set-store` (step 2) |
| "Kroger is limiting requests" / `429` | too many requests in a burst | wait a minute; the page pauses on its own |
| "no cart yet" | the account has never had a cart | add any item on kroger.com once |
| every request fails with `INTERNAL_ERROR` or `stream error` | the whole session aged out | sign in again from Chrome |
| "Kroger Cart isn't running" on the page | the server was stopped | double-click Kroger Cart, or run `kroger-pp-cli serve` |
| `serve` says `address already in use` | it's already running | use the open tab, or `--addr 127.0.0.1:9000` |

`--dry-run` on any command shows the request without sending it, with the
session masked.
