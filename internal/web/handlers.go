package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bschm0622/kroger-cart/internal/browser"
	"github.com/bschm0622/kroger-cart/internal/config"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		s.fail(w, err)
		return
	}
	store := storeInfo(cfg.Headers)
	writeJSON(w, map[string]any{
		"configPath":  cfg.Path,
		"staplesPath": s.paths.staples,
		"recipesPath": s.paths.recipes,
		"hasCookie":   cfg.CookieHeader() != "",
		"hasStore":    cfg.Headers["x-laf-object"] != "",
		"store":       store,
	})
}

type store struct {
	Name     string `json:"name,omitempty"`
	ID       string `json:"id,omitempty"`
	Modality string `json:"modality"`
}

func storeInfo(h map[string]string) store {
	st := store{Modality: "PICKUP"}
	var m struct {
		Type       string `json:"type"`
		LocationID string `json:"locationId"`
	}
	if json.Unmarshal([]byte(h["x-modality"]), &m) == nil {
		if m.Type != "" {
			st.Modality = m.Type
		}
		st.ID = m.LocationID
	}
	var laf []struct {
		Modality struct {
			HandoffAddress struct {
				Address struct {
					Name string `json:"name"`
				} `json:"address"`
			} `json:"handoffAddress"`
		} `json:"modality"`
	}
	if json.Unmarshal([]byte(h["x-laf-object"]), &laf) == nil && len(laf) > 0 {
		st.Name = laf[0].Modality.HandoffAddress.Address.Name
	}
	return st
}

func (s *Server) handleCookie(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cookie string `json:"cookie"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	raw := strings.TrimSpace(body.Cookie)
	raw = strings.TrimPrefix(raw, "Cookie:")
	raw = strings.TrimPrefix(raw, "cookie:")
	raw = strings.Join(strings.Fields(raw), " ")
	if !config.LooksLikeCookieJar(raw) {
		badRequest(w, fmt.Errorf("that doesn't look like a Cookie header (expected \"name=value; name=value\"). "+
			"Copy the Cookie: request header from DevTools → Network, not an Authorization value"))
		return
	}
	if err := s.saveJar(raw); err != nil {
		s.fail(w, err)
		return
	}
	s.answerWithCheck(w, r, map[string]any{"saved": true, "cookieCount": len(strings.Split(raw, ";"))})
}

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
		s.fail(w, err)
		return
	}
	s.answerWithCheck(w, r, map[string]any{"saved": true, "profile": chosen.Name})
}

// answerWithCheck reads the cart with the session just saved, so "signed in"
// is only said when it's true: Chrome may hold a stale session too, or not
// have written a fresh one to disk yet.
func (s *Server) answerWithCheck(w http.ResponseWriter, r *http.Request, resp map[string]any) {
	resp["working"] = false
	a, err := s.api()
	if err == nil {
		_, _, err = a.Cart(r.Context())
	}
	if err != nil {
		s.noteRateLimit(err)
		f := classify(err)
		resp["kind"], resp["error"] = f.Kind, f.Msg
	} else {
		resp["working"] = true
	}
	writeJSON(w, resp)
}

func (s *Server) saveJar(raw string) error {
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		return err
	}
	cfg.AuthHeaderVal = ""
	return cfg.SaveTokens("", "", raw, "", time.Time{})
}

// --- cart ---

func (s *Server) handleCartGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.api()
	if err != nil {
		s.fail(w, err)
		return
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	names, _, err := a.Products(r.Context(), upcsOf(lines))
	s.noteRateLimit(err)
	for i := range lines {
		if it, ok := names[lines[i].UPC]; ok {
			lines[i].Name = it.Name
			lines[i].Unavailable = it.Unavailable
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

type skip struct {
	UPC    string `json:"upc"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// handleCartUpdate takes the quantity each product should end up at (0
// removes it). Adds and increases are refused for anything Kroger can't sell;
// lowering or removing is always allowed.
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
	a, err := s.api()
	if err != nil {
		s.fail(w, err)
		return
	}
	if body.Modality == "" {
		body.Modality = a.modality()
	}
	id, lines, err := a.Cart(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	existing := map[string][]CartLine{}
	have := map[string]int{}
	for _, l := range lines {
		existing[l.UPC] = append(existing[l.UPC], l)
		have[l.UPC] += l.Quantity
	}

	var raised []string
	for _, upc := range order {
		if want[upc] > have[upc] {
			raised = append(raised, upc)
		}
	}
	skipped := []skip{}
	if len(raised) > 0 {
		details, unchecked, err := a.Products(r.Context(), raised)
		s.noteRateLimit(err)
		isUnchecked := map[string]bool{}
		for _, u := range unchecked {
			isUnchecked[u] = true
		}
		kept := order[:0]
		for _, upc := range order {
			if want[upc] > have[upc] && !isUnchecked[upc] {
				d, ok := details[upc]
				switch {
				case !ok:
					skipped = append(skipped, skip{UPC: upc, Reason: "Not found at your store"})
					continue
				case d.Unavailable != "":
					skipped = append(skipped, skip{UPC: upc, Name: d.Name, Reason: d.Unavailable})
					continue
				}
			}
			kept = append(kept, upc)
		}
		order = kept
	}

	var edits []lineEdit
	var adds []wantLine
	for _, upc := range order {
		q := want[upc]
		if len(existing[upc]) == 0 {
			if q > 0 {
				adds = append(adds, wantLine{UPC: upc, Quantity: q})
			}
			continue
		}
		for i, l := range existing[upc] {
			if i > 0 {
				q = 0
			}
			edits = append(edits, lineEdit{Line: l, Quantity: q})
		}
	}
	if err := a.EditLines(r.Context(), id, edits); err != nil {
		s.fail(w, err)
		return
	}
	if len(adds) > 0 {
		if err := a.AddToCart(r.Context(), id, adds, body.Modality); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, map[string]any{"changed": len(order), "skipped": skipped})
}

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
		s.fail(w, err)
		return
	}
	items, unchecked, err := a.Products(r.Context(), body.UPCs)
	if err != nil && len(items) == 0 {
		s.fail(w, err)
		return
	}
	s.noteRateLimit(err)
	if unchecked == nil {
		unchecked = []string{}
	}
	writeJSON(w, map[string]any{"items": items, "unchecked": unchecked})
}

// --- staples ---

func staplesVersion(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

func (s *Server) handleStaplesGet(w http.ResponseWriter, r *http.Request) {
	s.staplesMu.Lock()
	defer s.staplesMu.Unlock()
	items, err := LoadStaples(s.paths.staples)
	if err != nil {
		writeFailure(w, http.StatusConflict, failure{Kind: "staples_unreadable", Msg: err.Error()})
		return
	}
	writeJSON(w, map[string]any{"staples": items, "path": s.paths.staples, "version": staplesVersion(s.paths.staples)})
}

// handleStaplesPut refuses to write over a file that changed since the page
// read it (a hand edit, or kroger-staples --force), rather than losing it.
func (s *Server) handleStaplesPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Staples []Staple `json:"staples"`
		Version int64    `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	s.staplesMu.Lock()
	defer s.staplesMu.Unlock()
	if v := staplesVersion(s.paths.staples); v != body.Version {
		writeFailure(w, http.StatusConflict, failure{Kind: "staples_changed",
			Msg: "Your staples file changed outside this page, so it was reloaded instead of being overwritten. Make that change again."})
		return
	}
	if err := SaveStaples(s.paths.staples, body.Staples); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"saved": len(body.Staples), "version": staplesVersion(s.paths.staples)})
}

// handleStaplesSeed rebuilds the list from purchase history, keeping the
// quantities, usual marks and hand-added items already in it.
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
	s.staplesMu.Lock()
	defer s.staplesMu.Unlock()
	prior, err := LoadStaples(s.paths.staples)
	if err != nil {
		writeFailure(w, http.StatusConflict, failure{Kind: "staples_unreadable",
			Msg: "Your staples file couldn't be read, so it wasn't rebuilt over: " + err.Error()})
		return
	}
	a, err := s.api()
	if err != nil {
		s.fail(w, err)
		return
	}
	trips, err := a.History(r.Context(), body.Trips)
	if err != nil {
		s.fail(w, err)
		return
	}
	existing := map[string]Staple{}
	for _, p := range prior {
		existing[p.UPC] = p
	}
	freq := frequentUPCs(trips)
	var candidates []string
	for _, f := range freq {
		if f.Times >= body.MinTimes {
			candidates = append(candidates, f.UPC)
		}
	}
	items, unchecked, err := a.Products(r.Context(), candidates)
	if err != nil || len(unchecked) > 0 {
		s.fail(w, fmt.Errorf("couldn't look up every product, so nothing was changed: %w", err))
		return
	}

	seeded := make([]Staple, 0, len(freq))
	inSeeded := map[string]bool{}
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
		inSeeded[f.UPC] = true
	}
	for _, p := range prior {
		if !inSeeded[p.UPC] {
			seeded = append(seeded, p)
		}
	}
	if err := SaveStaples(s.paths.staples, seeded); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"staples": seeded, "trips": len(trips), "version": staplesVersion(s.paths.staples)})
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
		s.fail(w, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	items, err := a.Search(r.Context(), q, size)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	a, err := s.api()
	if err != nil {
		s.fail(w, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 25
	}
	trips, err := a.History(r.Context(), size)
	if err != nil {
		s.fail(w, err)
		return
	}
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
	items, unchecked, err := a.Products(r.Context(), all)
	if err != nil && len(items) == 0 {
		s.fail(w, err)
		return
	}
	s.noteRateLimit(err)
	if unchecked == nil {
		unchecked = []string{}
	}
	writeJSON(w, map[string]any{"trips": trips, "items": items, "unchecked": unchecked})
}
