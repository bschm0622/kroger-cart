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
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func newTestServer(t *testing.T, fake *fakeKroger) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
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
	r.RemoteAddr = "127.0.0.1:54321"
	r.Host = "127.0.0.1:8787"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
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

func TestCartUpdateAddsNewAndEditsExistingByLineID(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cart/update", `{"items":[
		{"upc":"0001111041660","qty":2},
		{"upc":"0000000004011","qty":0}]}`)
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var edit, add map[string]any
	for _, put := range fake.puts {
		for _, it := range put["lineItems"].([]any) {
			m := it.(map[string]any)
			switch m["gtin13"] {
			case "0000000004011":
				edit = m
			case "0001111041660":
				add = m
			}
		}
	}
	if edit == nil || edit["id"] != "line-1" || edit["quantity"] != float64(0) {
		t.Errorf("removal went out as %v; want quantity 0 on line-1", edit)
	}
	if add == nil || add["quantity"] != float64(2) {
		t.Fatalf("add went out as %v; want quantity 2", add)
	}
	if _, hasID := add["id"]; hasID {
		t.Errorf("a fresh add carries an id: %v", add)
	}
	if add["modalityType"] != "PICKUP" || add["channel"] != "WEB" || add["substitutionPolicy"] != "SHOPPER_CHOICE" {
		t.Errorf("add is missing required fields: %v", add)
	}
}

func TestCartUpdateRejectsNoChanges(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)
	if w := do(t, srv, "POST", "/api/cart/update", `{"items":[]}`); w.Code != 400 {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestStaplesSeedCountsTripsAndKeepsEdits(t *testing.T) {
	fake := newFakeKroger(t)
	srv, dir := newTestServer(t, fake)

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
	items := out["items"].(map[string]any)
	if b, _ := items["0000000004011"].(map[string]any); b == nil || b["name"] != "Bananas" {
		t.Errorf("items came back as %v", items)
	}
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

func TestCrossOriginRequestIsRefused(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	send := func(origin string) int {
		r := httptest.NewRequest("POST", "/api/cart/update", strings.NewReader(`{"items":[{"upc":"0000000004011","qty":0}]}`))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = "127.0.0.1:8787"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for _, origin := range []string{"https://evil.example", "http://kroger.com", "http://localhost:3000", "http://127.0.0.1:9999"} {
		if code := send(origin); code != http.StatusForbidden {
			t.Errorf("origin %s got %d, want 403", origin, code)
		}
	}
	if code := send("http://127.0.0.1:8787"); code == http.StatusForbidden {
		t.Errorf("the page's own origin was refused")
	}
}

func TestCookieEndpointRejectsNonCookie(t *testing.T) {
	fake := newFakeKroger(t)
	srv, _ := newTestServer(t, fake)

	w := do(t, srv, "POST", "/api/cookie", `{"cookie":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc.def"}`)
	if w.Code != 400 {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
}

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
	deli, ok := byUPC["0023536740000"]
	if !ok {
		t.Fatal("the deli item was dropped; only non-grocery departments should be")
	}
	if deli.Name != "Smoked Turkey" {
		t.Errorf("deli name = %q, want it resolved by the unverified retry", deli.Name)
	}
}
