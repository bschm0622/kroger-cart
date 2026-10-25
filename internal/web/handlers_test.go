package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bschm0622/kroger-cart/internal/client"
	"github.com/bschm0622/kroger-cart/internal/config"
)

// fakeKroger stands in for the Atlas API so the cart wire shapes can be pinned
// without a live session. It records the PUT bodies the UI sends, which is the
// part most likely to break: an empty lineItems array or a missing line item id
// is rejected by the real API, and both are easy to reintroduce.
type fakeKroger struct {
	*httptest.Server
	puts []map[string]any
	seen []string
}

func newFakeKroger(t *testing.T) *fakeKroger {
	t.Helper()
	f := &fakeKroger{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/carts":
			_, _ = w.Write([]byte(`{"data":{"carts":[
				{"id":"saved","type":"SAVED_FOR_LATER","lineItems":[{"id":"s1","gtin13":"999","quantity":1}]},
				{"id":"cart-1","type":"ACTIVE","lineItems":[
					{"id":"line-1","gtin13":"0000000004011","quantity":2,"modalityType":"PICKUP"}]}]}}`))

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/atlas/v1/carts/"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("cart PUT body did not parse: %v", err)
			}
			f.puts = append(f.puts, body)
			_, _ = w.Write([]byte(`{"data":{"cart":{"id":"cart-1"}}}`))

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/product/v2/products":
			// The verified catalog omits the deli item and the pickup fee, so
			// they only appear on the unverified retry — same as the real API.
			if r.URL.Query().Get("filter.verified") == "true" {
				_, _ = w.Write([]byte(`{"data":{"products":[
					{"item":{"upc":"0000000004011","description":"Bananas","customerFacingSize":"1 Bunch"}},
					{"item":{"upc":"0001111041660","description":"Milk"}}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"products":[
				{"item":{"upc":"0000000004011","description":"Bananas","customerFacingSize":"1 Bunch"}},
				{"item":{"upc":"0001111041660","description":"Milk"}},
				{"item":{"upc":"0023536740000","description":"Smoked Turkey",
				 "familyTree":{"department":{"code":"15","name":"DELI BULK"}}}},
				{"item":{"upc":"0000000023108","description":"IN STORE PICKUP",
				 "familyTree":{"department":{"code":"34","name":"MISC SALES TRAN"}}}}]}}`))

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/search/v1/products-search":
			_, _ = w.Write([]byte(`{"data":{"productsSearch":[
				{"upc":"0001111004412","description":"kroger extra virgin olive oil"}]}}`))

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/post-order/v1/purchase-history-search":
			_, _ = w.Write([]byte(`{"data":{"postOrderSearch":{"data":[
				{"createdDateTime":{"value":"2026-07-28T15:20:00Z"},"total":"USD 12.37",
				 "purchaseType":"IN_STORE","receiptKey":"021~00744~2026-07-28~10~61120",
				 "lineItems":[{"upc":"0000000004011"},{"upc":"0001111041660"},{"upc":"0000000004011"},
				  {"upc":"0000000023108"},{"upc":"0023536740000"}]},
				{"createdDateTime":{"value":"2026-07-15T18:16:00Z"},"total":"USD 23.34",
				 "purchaseType":"IN_STORE","receiptKey":"021~00744~2026-07-15~13~971416",
				 "lineItems":[{"upc":"0000000004011"}]}]}}}`))

		default:
			// Kroger answers a wrong method with 405, not a 404 or a body, so
			// mismatches land here rather than parsing as an empty result.
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// newTestServer wires a Server against the fake API and a scratch config dir.
func newTestServer(t *testing.T, fake *fakeKroger) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// A cookie-shaped value so CookieHeader() is non-empty; the fake ignores it.
	body := "base_url = \"" + fake.URL + "\"\naccess_token = \"bm_sz=abc; _abck=def\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer("127.0.0.1:0", cfgPath, func() (*client.Client, error) {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return nil, err
		}
		c := client.New(cfg, 10*time.Second, 0)
		c.NoCache = true
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, dir
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	// httptest.NewRequest's default RemoteAddr is 192.0.2.1, which localOnly
	// correctly rejects; every real request arrives from loopback.
	r.RemoteAddr = "127.0.0.1:54321"
	// Its default Host, example.com, is refused too: real requests are
	// addressed to the address the server listens on.
	r.Host = "127.0.0.1:8787"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not JSON (%d): %s", w.Code, w.Body.String())
	}
	return out
}

// The cart read must pick the ACTIVE cart and resolve names, because a cart of
// bare barcodes can't be reviewed before checkout.
func TestCartGetPicksActiveCartAndResolvesNames(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "GET", "/api/cart", "")
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	out := decodeBody(t, w)
	if out["cartId"] != "cart-1" {
		t.Errorf("got cartId %v, want cart-1 (not the saved-for-later cart)", out["cartId"])
	}
	lines := out["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	line := lines[0].(map[string]any)
	if line["name"] != "Bananas" || line["id"] != "line-1" {
		t.Errorf("line came back as %+v", line)
	}
}

// Adding must send a quantity per UPC with no line item id, and must merge a
// UPC requested twice — two recipes both wanting onions is the normal case.
func TestCartAddMergesDuplicatesAndOmitsLineID(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cart/add", `{"items":[
		{"upc":"0001111062960","qty":1},
		{"upc":"0001111041660","qty":1},
		{"upc":"0001111062960","qty":3}]}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.puts) != 1 {
		t.Fatalf("got %d cart writes, want 1", len(fake.puts))
	}
	items := fake.puts[0]["lineItems"].([]any)
	if len(items) != 2 {
		t.Fatalf("got %d line items, want 2 after merging: %v", len(items), items)
	}
	byUPC := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byUPC[m["gtin13"].(string)] = m
	}
	if q := byUPC["0001111062960"]["quantity"]; q != float64(3) {
		t.Errorf("merged quantity is %v, want the larger value 3", q)
	}
	for upc, m := range byUPC {
		if _, hasID := m["id"]; hasID {
			t.Errorf("%s carries an id; a fresh add must omit it", upc)
		}
		if m["modalityType"] != "PICKUP" || m["channel"] != "WEB" || m["substitutionPolicy"] != "SHOPPER_CHOICE" {
			t.Errorf("%s is missing required fields: %v", upc, m)
		}
	}
}

// Clearing is quantity 0 against each server-assigned id. An empty lineItems
// array is rejected by the real API with "must contain at least 1 items", so
// the removal has to be spelled out line by line.
func TestCartClearUsesZeroQuantityWithLineIDs(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	if w := do(t, srv, "POST", "/api/cart/clear", ""); w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.puts) != 1 {
		t.Fatalf("got %d cart writes, want 1", len(fake.puts))
	}
	items := fake.puts[0]["lineItems"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	m := items[0].(map[string]any)
	if m["quantity"] != float64(0) || m["id"] != "line-1" {
		t.Errorf("clear sent %v; want quantity 0 with id line-1", m)
	}
}

// "Empty cart first" must clear before adding, not after — the other order
// would delete the week's order it just placed.
func TestCartAddWithClearClearsFirst(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cart/add", `{"items":[{"upc":"0001111041660","qty":1}],"clear":true}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.puts) != 2 {
		t.Fatalf("got %d cart writes, want 2 (clear then add)", len(fake.puts))
	}
	first := fake.puts[0]["lineItems"].([]any)[0].(map[string]any)
	if first["quantity"] != float64(0) {
		t.Errorf("first write was not the clear: %v", first)
	}
	second := fake.puts[1]["lineItems"].([]any)[0].(map[string]any)
	if second["quantity"] != float64(1) || second["gtin13"] != "0001111041660" {
		t.Errorf("second write was not the add: %v", second)
	}
}

func TestCartAddRejectsEmptySelection(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)
	if w := do(t, srv, "POST", "/api/cart/add", `{"items":[]}`); w.Code != 400 {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
	if len(fake.puts) != 0 {
		t.Fatal("an empty selection reached the cart")
	}
}

// Seeding counts trips, not line items: a UPC listed twice on one receipt is
// still one trip, or a single bulk purchase would outrank a weekly habit.
func TestStaplesSeedCountsTripsAndKeepsEdits(t *testing.T) {
	fake := newFakeKroger(t)
	srv, dir := newTestServer(t, fake)

	// A prior list with a curated quantity and selection, plus a hand-added
	// item that no trip mentions.
	prior := staplesHeader + "\n" +
		"3\t0000000004011\t28x\tBananas\n" +
		"# 1\t0009999999999\t0x\tHand added\n"
	if err := os.WriteFile(filepath.Join(dir, "staples.tsv"), []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv, "POST", "/api/staples/seed", `{"trips":100,"minTimes":1}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	items, err := LoadStaples(filepath.Join(dir, "staples.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	byUPC := map[string]Staple{}
	for _, i := range items {
		byUPC[i.UPC] = i
	}
	if b := byUPC["0000000004011"]; b.Times != 2 {
		t.Errorf("bananas counted %d times; want 2 trips (it appears twice on one receipt)", b.Times)
	}
	if b := byUPC["0000000004011"]; b.Quantity != 3 || !b.Selected {
		t.Errorf("re-seeding lost a curated quantity/selection: %+v", b)
	}
	if _, ok := byUPC["0009999999999"]; !ok {
		t.Error("re-seeding dropped a hand-added staple that no trip mentions")
	}
	if m := byUPC["0001111041660"]; m.Name != "Milk" {
		t.Errorf("a newly seeded item did not get its name resolved: %+v", m)
	}
}

func TestHistoryResolvesNamesInOneBulkCall(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "GET", "/api/history?size=25", "")
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	out := decodeBody(t, w)
	if len(out["trips"].([]any)) != 2 {
		t.Fatalf("got %v trips", out["trips"])
	}
	names := out["names"].(map[string]any)
	if names["0000000004011"] != "Bananas" {
		t.Errorf("names came back as %v", names)
	}
	// The same UPC appears three times across the two trips; resolving per
	// trip would multiply a rate-limited call needlessly. One verified call
	// plus at most one unverified retry for whatever it missed is the ceiling,
	// however many trips or repeats there are.
	var verified, unverified int
	for _, req := range fake.seen {
		switch {
		case !strings.Contains(req, "/product/v2/products"):
		case strings.Contains(req, "filter.verified=true"):
			verified++
		default:
			unverified++
		}
	}
	if verified != 1 || unverified > 1 {
		t.Errorf("made %d verified and %d unverified lookups for 2 trips; want 1 and at most 1",
			verified, unverified)
	}
	first := out["trips"].([]any)[0].(map[string]any)
	if first["date"] != "2026-07-28" || first["total"] != "12.37" {
		t.Errorf("trip fields came back as %v", first)
	}
}

func TestSearchNeedsATerm(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)
	if w := do(t, srv, "GET", "/api/search?q=%20", ""); w.Code != 400 {
		t.Fatalf("got %d, want 400", w.Code)
	}
	w := do(t, srv, "GET", "/api/search?q=olive+oil", "")
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	items := decodeBody(t, w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["upc"] != "0001111004412" {
		t.Fatalf("got %v", items)
	}
	// The wire parameter is filter.query; filter.keyword is ignored rather
	// than rejected and silently returns nothing.
	var found bool
	for _, req := range fake.seen {
		if strings.Contains(req, "filter.query=olive+oil") {
			found = true
		}
	}
	if !found {
		t.Errorf("search did not send filter.query: %v", fake.seen)
	}
}

// The recipe editor's round trip has to survive DisallowUnknownFields, so the
// JSON the page posts must match the struct exactly.
func TestRecipeAPIRoundTrip(t *testing.T) {
	fake := newFakeKroger(t)
	srv, dir := newTestServer(t, fake)

	w := do(t, srv, "PUT", "/api/recipes",
		`{"slug":"","name":"Turkey Tacos","notes":"Tuesday","ingredients":[{"upc":"0001111066275","qty":2,"name":"Turkey"}]}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if slug := decodeBody(t, w)["slug"]; slug != "turkey-tacos" {
		t.Fatalf("got slug %v", slug)
	}

	w = do(t, srv, "GET", "/api/recipes", "")
	recipes := decodeBody(t, w)["recipes"].([]any)
	if len(recipes) != 1 {
		t.Fatalf("got %d recipes", len(recipes))
	}
	if _, err := os.Stat(filepath.Join(dir, "recipes", "turkey-tacos.toml")); err != nil {
		t.Fatalf("recipe file not written: %v", err)
	}

	if w := do(t, srv, "POST", "/api/recipes/delete", `{"slug":"turkey-tacos"}`); w.Code != 200 {
		t.Fatalf("delete got %d: %s", w.Code, w.Body.String())
	}
}

// The saved session can place orders, so a request from off-box must not be
// served even if someone binds to a routable address.
func TestNonLoopbackRequestIsRefused(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	r := httptest.NewRequest("GET", "/api/cart", nil)
	r.RemoteAddr = "203.0.113.9:4000"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}

// A page on another origin must not be able to drive the API through the
// browser; localhost binding alone does not stop that.
func TestCrossOriginRequestIsRefused(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	for _, origin := range []string{"https://evil.example", "http://kroger.com"} {
		r := httptest.NewRequest("POST", "/api/cart/clear", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("origin %s got %d, want 403", origin, w.Code)
		}
	}
	for _, origin := range []string{"http://localhost:8787", "http://127.0.0.1:8787"} {
		r := httptest.NewRequest("GET", "/api/cart", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = "127.0.0.1:8787"
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code == http.StatusForbidden {
			t.Errorf("origin %s was refused", origin)
		}
	}
}

// A pasted bearer token must be rejected rather than stored: it would overwrite
// a working cookie jar and every later call would 401.
func TestCookieEndpointRejectsNonCookie(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cookie", `{"cookie":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc.def"}`)
	if w.Code != 400 {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
}

// DevTools' "copy value" includes the header name on some wrapped headers and
// breaks long values across lines; both must be tolerated.
func TestCookieEndpointNormalizesPaste(t *testing.T) {
	fake := newFakeKroger(t)
	srv, dir := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cookie", `{"cookie":"Cookie: bm_sz=abc;\n  _abck=def; xyz=1"}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.CookieHeader()
	if strings.Contains(got, "Cookie:") || strings.Contains(got, "\n") {
		t.Fatalf("stored value was not normalized: %q", got)
	}
	if got != "bm_sz=abc; _abck=def; xyz=1" {
		t.Fatalf("got %q", got)
	}
}

// The in-store pickup fee rides along on every pickup order, so by trip count
// it outranks every real grocery. It is a transaction line, not something that
// can be put in a cart, and must not head the staples list.
func TestStaplesSeedDropsNonGroceryDepartments(t *testing.T) {
	fake := newFakeKroger(t)
	srv, dir := newTestServer(t, fake)

	if w := do(t, srv, "POST", "/api/staples/seed", `{"trips":100,"minTimes":1}`); w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	items, err := LoadStaples(filepath.Join(dir, "staples.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	byUPC := map[string]Staple{}
	for _, i := range items {
		byUPC[i.UPC] = i
	}
	if _, ok := byUPC["0000000023108"]; ok {
		t.Error("the in-store pickup fee (department 34) was seeded as a staple")
	}
	// The deli item is a real grocery the verified catalog does not carry. It
	// must survive, and the unverified retry must supply its name — otherwise
	// the list shows a bare barcode.
	deli, ok := byUPC["0023536740000"]
	if !ok {
		t.Fatal("the deli item was dropped; only non-grocery departments should be")
	}
	if deli.Name != "Smoked Turkey" {
		t.Errorf("deli name = %q, want it resolved by the unverified retry", deli.Name)
	}
}
