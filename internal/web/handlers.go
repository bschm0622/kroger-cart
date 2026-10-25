package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bschm0622/kroger-cart/internal/browser"
	"github.com/bschm0622/kroger-cart/internal/config"
)

// handleStatus reports whether the saved session still works, which is the
// first thing the page asks on load.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		fail(w, err)
		return
	}
	resp := map[string]any{
		"configPath":  cfg.Path,
		"staplesPath": s.paths.staples,
		"recipesPath": s.paths.recipes,
		"hasCookie":   cfg.CookieHeader() != "",
	}
	if cfg.CookieHeader() == "" {
		resp["ok"] = false
		resp["detail"] = "No session saved yet."
		writeJSON(w, resp)
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	if _, _, err := a.Cart(r.Context()); err != nil {
		resp["ok"] = false
		resp["detail"] = err.Error()
		// A reset stream, a 401, and a 403 all mean the same thing here — the
		// jar needs replacing — and all three have the same one-step fix, so
		// the page should say that rather than quote a transport error.
		for _, marker := range []string{"401", "403", "INTERNAL_ERROR", "stream error"} {
			if strings.Contains(err.Error(), marker) {
				resp["detail"] = "The saved session is no longer valid. Paste a fresh Cookie header."
				break
			}
		}
		writeJSON(w, resp)
		return
	}
	resp["ok"] = true
	writeJSON(w, resp)
}

// handleCookie saves a Cookie header pasted into the UI.
//
// This is the whole reason the paste box exists: the cookie has to be copied
// out of DevTools in the browser anyway, so pasting it into a page in that same
// browser removes the terminal from the weekly routine entirely.
func (s *Server) handleCookie(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cookie string `json:"cookie"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	// Tolerate a paste that includes the header name, and collapse the
	// newlines DevTools' "copy value" adds to a wrapped header.
	raw := strings.TrimSpace(body.Cookie)
	raw = strings.TrimPrefix(raw, "Cookie:")
	raw = strings.TrimPrefix(raw, "cookie:")
	raw = strings.Join(strings.Fields(raw), " ")

	if !config.LooksLikeCookieJar(raw) {
		badRequest(w, fmt.Errorf("that doesn't look like a Cookie header (expected \"name=value; name=value\"). "+
			"Copy the Cookie: request header from DevTools → Network — not an Authorization value"))
		return
	}
	if err := s.saveJar(raw); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"saved": true, "cookieCount": len(strings.Split(raw, ";"))})
}

// handleCookieFromChrome reads the session straight out of Chrome, which skips
// the paste box entirely.
//
// The cookies Kroger needs are on disk and encrypted with a key in the login
// keychain, so the DevTools ritual was never actually necessary on macOS. It
// stays as the fallback for other platforms and other browsers.
func (s *Server) handleCookieFromChrome(w http.ResponseWriter, r *http.Request) {
	jar, chosen, err := browser.ChromeJar("")
	if err != nil {
		badRequest(w, err)
		return
	}
	if !config.LooksLikeCookieJar(jar) {
		badRequest(w, fmt.Errorf("the jar read from Chrome profile %q does not look like a Cookie header", chosen.Name))
		return
	}
	if err := s.saveJar(jar); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"saved":       true,
		"cookieCount": len(strings.Split(jar, ";")),
		"profile":     chosen.Name,
	})
}

// saveJar persists a cookie header as the active session.
func (s *Server) saveJar(raw string) error {
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		return err
	}
	// Clear auth_header so CookieHeader() reads what we just stored rather
	// than a stale hand-edited override. The expiry is zero because a captured
	// session's real lifetime isn't knowable here; it surfaces as a 401 later.
	cfg.AuthHeaderVal = ""
	return cfg.SaveTokens("", "", raw, "", time.Time{})
}

// --- cart ---

func (s *Server) handleCartGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	// Names come from a second call: the cart body carries only UPCs, and a
	// cart of bare barcodes is unreviewable.
	if names, err := a.Products(r.Context(), upcsOf(lines)); err == nil {
		for i := range lines {
			if it, ok := names[lines[i].UPC]; ok {
				lines[i].Name = it.Name
				lines[i].Unavailable = it.Unavailable
			}
		}
	}
	writeJSON(w, map[string]any{"cartId": id, "lines": lines})
}

func upcsOf(lines []CartLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.UPC)
	}
	return out
}

func (s *Server) handleCartAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items    []wantLine `json:"items"`
		Modality string     `json:"modality"`
		Clear    bool       `json:"clear"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if len(body.Items) == 0 {
		badRequest(w, fmt.Errorf("nothing selected"))
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if body.Clear {
		if err := a.ClearCart(r.Context(), id, lines); err != nil {
			fail(w, err)
			return
		}
	}
	// Duplicate UPCs would send two lines for one product and let the API pick
	// a winner. Merging by taking the larger quantity makes "staples + two
	// recipes that both want onions" behave the way the page implies.
	want := mergeWants(body.Items)
	if !body.Clear {
		// Kroger sets a quantity rather than adding to it, so sending 1 of
		// something already in the cart twice would quietly drop it to 1.
		// Adding never lowers what's there; lowering is done on kroger.com.
		have := map[string]int{}
		for _, l := range lines {
			have[l.UPC] += l.Quantity
		}
		for i := range want {
			want[i].Quantity = max(want[i].Quantity, have[want[i].UPC])
		}
	}
	if err := a.AddToCart(r.Context(), id, want, body.Modality); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"added": len(want), "cartId": id})
}

func mergeWants(items []wantLine) []wantLine {
	byUPC := map[string]int{}
	var order []string
	for _, it := range items {
		if it.UPC == "" {
			continue
		}
		if _, seen := byUPC[it.UPC]; !seen {
			order = append(order, it.UPC)
		}
		byUPC[it.UPC] = max(byUPC[it.UPC], max(it.Quantity, 1))
	}
	out := make([]wantLine, 0, len(order))
	for _, u := range order {
		out = append(out, wantLine{UPC: u, Quantity: byUPC[u]})
	}
	return out
}

// handleCartUpdate applies the page's pending changes: each item is the
// quantity that product should end up at, 0 meaning remove it. Products
// already in the cart are edited by line id; new ones are added.
func (s *Server) handleCartUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items    []wantLine `json:"items"`
		Modality string     `json:"modality"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if len(body.Items) == 0 {
		badRequest(w, fmt.Errorf("no changes to send"))
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	existing := map[string][]CartLine{}
	for _, l := range lines {
		existing[l.UPC] = append(existing[l.UPC], l)
	}
	// Last word per UPC wins, in the order first asked for.
	want := map[string]int{}
	var order []string
	for _, it := range body.Items {
		if it.UPC == "" || it.Quantity < 0 {
			badRequest(w, fmt.Errorf("bad change for %q", it.UPC))
			return
		}
		if _, seen := want[it.UPC]; !seen {
			order = append(order, it.UPC)
		}
		want[it.UPC] = it.Quantity
	}
	// Nothing goes into the cart that Kroger says it can't sell. Only adds
	// and increases are checked: lowering or removing an out-of-stock item
	// already in the cart is always allowed. If the lookup itself fails the
	// update goes ahead unchecked rather than blocking the whole cart.
	have := func(upc string) int {
		n := 0
		for _, l := range existing[upc] {
			n += l.Quantity
		}
		return n
	}
	var raised []string
	for _, upc := range order {
		if want[upc] > have(upc) {
			raised = append(raised, upc)
		}
	}
	type skip struct {
		UPC    string `json:"upc"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	var skipped []skip
	if len(raised) > 0 {
		if details, err := a.Products(r.Context(), raised); err == nil {
			kept := order[:0]
			for _, upc := range order {
				if d, ok := details[upc]; ok && d.Unavailable != "" && want[upc] > have(upc) {
					skipped = append(skipped, skip{UPC: upc, Name: d.Name, Reason: d.Unavailable})
					continue
				}
				kept = append(kept, upc)
			}
			order = kept
		}
	}

	var edits []lineEdit
	var adds []wantLine
	for _, upc := range order {
		q := want[upc]
		have := existing[upc]
		if len(have) == 0 {
			if q > 0 {
				adds = append(adds, wantLine{UPC: upc, Quantity: q})
			}
			continue
		}
		// A product can sit on more than one line (pickup and delivery, say);
		// the first carries the new quantity and any others go.
		for i, l := range have {
			if i > 0 {
				q = 0
			}
			edits = append(edits, lineEdit{Line: l, Quantity: q})
		}
	}
	if err := a.EditLines(r.Context(), id, edits); err != nil {
		fail(w, err)
		return
	}
	if len(adds) > 0 {
		if err := a.AddToCart(r.Context(), id, adds, body.Modality); err != nil {
			fail(w, err)
			return
		}
	}
	if skipped == nil {
		skipped = []skip{}
	}
	writeJSON(w, map[string]any{"changed": len(order), "skipped": skipped})
}

// handleProducts looks up details — name, picture, price, availability — for
// UPCs the page already has, such as the staples list, which is a local file
// and knows none of that.
func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UPCs []string `json:"upcs"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if len(body.UPCs) > 500 {
		badRequest(w, fmt.Errorf("too many products at once (%d, max 500)", len(body.UPCs)))
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	items, err := a.Products(r.Context(), body.UPCs)
	if err != nil && len(items) == 0 {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) handleCartClear(w http.ResponseWriter, r *http.Request) {
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.ClearCart(r.Context(), id, lines); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"cleared": len(lines)})
}

func (s *Server) handleCartQuantity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UPC      string `json:"upc"`
		Quantity int    `json:"qty"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if body.Quantity < 0 {
		badRequest(w, fmt.Errorf("quantity cannot be negative"))
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	for _, l := range lines {
		if l.UPC != body.UPC {
			continue
		}
		if err := a.SetQuantity(r.Context(), id, l, body.Quantity); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	badRequest(w, fmt.Errorf("%s is not in the cart", body.UPC))
}

// --- staples ---

func (s *Server) handleStaplesGet(w http.ResponseWriter, r *http.Request) {
	items, err := LoadStaples(s.paths.staples)
	if err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, map[string]any{"staples": items, "path": s.paths.staples})
}

func (s *Server) handleStaplesPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Staples []Staple `json:"staples"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if err := SaveStaples(s.paths.staples, body.Staples); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"saved": len(body.Staples), "path": s.paths.staples})
}

// handleStaplesSeed rebuilds the master list from purchase history, keeping the
// quantities and selections already recorded for items that are already listed.
// Re-seeding after a few more trips should improve the counts, not reset the
// list you curated.
func (s *Server) handleStaplesSeed(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Trips    int `json:"trips"`
		MinTimes int `json:"minTimes"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if body.Trips <= 0 {
		body.Trips = 100
	}
	if body.MinTimes <= 0 {
		body.MinTimes = 2
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	trips, err := a.History(r.Context(), body.Trips)
	if err != nil {
		fail(w, err)
		return
	}
	existing := map[string]Staple{}
	if prior, err := LoadStaples(s.paths.staples); err == nil {
		for _, p := range prior {
			existing[p.UPC] = p
		}
	}

	var candidates []string
	freq := a.FrequentUPCs(r.Context(), trips)
	for _, f := range freq {
		if f.Times >= body.MinTimes {
			candidates = append(candidates, f.UPC)
		}
	}
	// Every candidate is looked up, not just the unnamed ones, because the
	// lookup is also what says whether a UPC is a grocery at all.
	items, err := a.Products(r.Context(), candidates)
	if err != nil && len(items) == 0 {
		fail(w, err)
		return
	}

	seeded := make([]Staple, 0, len(freq))
	for _, f := range freq {
		if f.Times < body.MinTimes {
			continue
		}
		it, known := items[f.UPC]
		if known && !it.Orderable() {
			continue
		}
		st := Staple{UPC: f.UPC, Quantity: 1, Times: f.Times, Name: it.Name}
		if prev, ok := existing[f.UPC]; ok {
			st.Quantity, st.Selected = prev.Quantity, prev.Selected
			if st.Name == "" {
				st.Name = prev.Name
			}
		}
		seeded = append(seeded, st)
	}
	// Keep hand-added staples that didn't clear the frequency threshold —
	// they were deliberate, and dropping them would silently un-add them.
	for _, p := range existing {
		if !containsUPC(seeded, p.UPC) {
			seeded = append(seeded, p)
		}
	}
	if err := SaveStaples(s.paths.staples, seeded); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"staples": seeded, "trips": len(trips)})
}

func containsUPC(items []Staple, upc string) bool {
	for _, i := range items {
		if i.UPC == upc {
			return true
		}
	}
	return false
}

// --- recipes ---

func (s *Server) handleRecipesGet(w http.ResponseWriter, r *http.Request) {
	recipes, err := LoadRecipes(s.paths.recipes)
	if err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, map[string]any{"recipes": recipes, "path": s.paths.recipes})
}

func (s *Server) handleRecipePut(w http.ResponseWriter, r *http.Request) {
	var recipe Recipe
	if err := decode(r, &recipe); err != nil {
		badRequest(w, err)
		return
	}
	saved, err := SaveRecipe(s.paths.recipes, recipe)
	if err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, saved)
}

func (s *Server) handleRecipeDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug string `json:"slug"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if err := DeleteRecipe(s.paths.recipes, body.Slug); err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, map[string]any{"deleted": body.Slug})
}

// --- lookup ---

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		badRequest(w, fmt.Errorf("search needs a q parameter"))
		return
	}
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	items, err := a.Search(r.Context(), q, size)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

// handleHistory returns trips with product names resolved, so a past trip can
// be reviewed and re-added as a unit.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	a, err := s.api()
	if err != nil {
		fail(w, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 25
	}
	trips, err := a.History(r.Context(), size)
	if err != nil {
		fail(w, err)
		return
	}
	// One bulk lookup for every UPC across every trip, deduplicated: a page of
	// 25 trips can easily reference the same milk ten times, and resolving per
	// trip would multiply a 2-rps-limited call by 25.
	seen := map[string]bool{}
	var all []string
	for _, t := range trips {
		for _, u := range t.UPCs {
			if !seen[u] {
				seen[u] = true
				all = append(all, u)
			}
		}
	}
	names, err := a.Products(r.Context(), all)
	if err != nil && len(names) == 0 {
		fail(w, err)
		return
	}
	flat := make(map[string]string, len(names))
	unavailable := map[string]string{}
	images := map[string]string{}
	for u, it := range names {
		flat[u] = it.Name
		if it.Unavailable != "" {
			unavailable[u] = it.Unavailable
		}
		if it.Image != "" {
			images[u] = it.Image
		}
	}
	writeJSON(w, map[string]any{"trips": trips, "names": flat, "unavailable": unavailable, "images": images})
}
