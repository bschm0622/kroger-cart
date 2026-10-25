// Command fakekroger stands in for kroger.com in the end-to-end tests, so they
// exercise the real page and server without touching anyone's account.
//
// It answers the five endpoints the page uses with a small fixed catalog, and
// keeps a cart that PUTs actually change. POST /__reset restores the starting
// cart; GET /__cart shows the current one, for tests to check what was sent.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type product struct {
	UPC, Name, Size, Price, Level string
	Sellable                      bool
	Message                       string
}

// The catalog. 0000000000104 is deliberately absent: a staple Kroger no longer
// carries comes back as nothing at all.
var catalog = map[string]product{
	"0000000000101": {UPC: "0000000000101", Name: "Test Whole Milk", Size: "1 gal", Price: "USD 3.49", Level: "HIGH", Sellable: true},
	"0000000000102": {UPC: "0000000000102", Name: "Test Bananas", Size: "1 bunch", Price: "USD 0.59", Level: "LOW", Sellable: true},
	"0000000000103": {UPC: "0000000000103", Name: "Test Olive Oil", Size: "16.9 fl oz", Price: "USD 8.99", Level: "TEMPORARILY_OUT_OF_STOCK", Message: "Item Out of Stock"},
	"0000000000105": {UPC: "0000000000105", Name: "Test Sourdough Bread", Size: "24 oz", Price: "USD 4.99", Level: "HIGH", Sellable: true},
	"0000000000106": {UPC: "0000000000106", Name: "Test Coffee Beans", Size: "12 oz", Price: "USD 9.99", Level: "TEMPORARILY_OUT_OF_STOCK", Message: "Item Out of Stock"},
}

// startCart holds the out-of-stock olive oil, so tests can check it can be
// removed but not raised.
func startCart() map[string]int { return map[string]int{"0000000000103": 1} }

// A 1×1 PNG, served as every product picture.
var pixel, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func main() {
	addr := flag.String("addr", "127.0.0.1:18788", "listen address")
	flag.Parse()
	base := "http://" + *addr

	var mu sync.Mutex
	cart := startCart()

	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	productJSON := func(p product) map[string]any {
		return map[string]any{
			"item": map[string]any{
				"upc": p.UPC, "description": p.Name, "customerFacingSize": p.Size,
				"images": []map[string]string{{"size": "medium", "url": base + "/img/" + p.UPC + ".png"}},
			},
			"fulfillmentSummaries": []map[string]any{{
				"type":             "PICKUP",
				"regular":          map[string]string{"price": p.Price},
				"availability":     map[string]any{"sellable": p.Sellable, "inventoryLevel": p.Level, "unavailabilityMessage": p.Message},
				"maxOrderQuantity": 6,
			}},
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/__reset":
			cart = startCart()
			write(w, map[string]bool{"ok": true})

		case r.Method == http.MethodGet && r.URL.Path == "/__cart":
			write(w, cart)

		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pixel)

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/carts":
			upcs := make([]string, 0, len(cart))
			for upc := range cart {
				upcs = append(upcs, upc)
			}
			sort.Strings(upcs)
			lines := []map[string]any{}
			for _, upc := range upcs {
				lines = append(lines, map[string]any{"id": "line-" + upc, "gtin13": upc, "quantity": cart[upc], "modalityType": "PICKUP"})
			}
			write(w, map[string]any{"data": map[string]any{"carts": []map[string]any{
				{"id": "cart-1", "type": "ACTIVE", "lineItems": lines}}}})

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/atlas/v1/carts/"):
			var body struct {
				LineItems []struct {
					ID       string `json:"id"`
					GTIN13   string `json:"gtin13"`
					Quantity int    `json:"quantity"`
				} `json:"lineItems"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for _, li := range body.LineItems {
				if li.Quantity <= 0 {
					delete(cart, li.GTIN13)
				} else {
					cart[li.GTIN13] = li.Quantity
				}
			}
			write(w, map[string]any{"data": map[string]any{"cart": map[string]string{"id": "cart-1"}}})

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/product/v2/products":
			out := []map[string]any{}
			for _, upc := range r.URL.Query()["filter.gtin13s"] {
				if p, ok := catalog[upc]; ok {
					out = append(out, productJSON(p))
				}
			}
			write(w, map[string]any{"data": map[string]any{"products": out}})

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/search/v1/products-search":
			write(w, map[string]any{"data": map[string]any{"productsSearch": []map[string]string{
				{"upc": "0000000000101", "description": "test whole milk"},
				{"upc": "0000000000103", "description": "test olive oil"},
				{"upc": "0000000000105", "description": "test sourdough bread"},
			}}})

		case r.Method == http.MethodGet && r.URL.Path == "/atlas/v1/post-order/v1/purchase-history-search":
			write(w, map[string]any{"data": map[string]any{"postOrderSearch": map[string]any{"data": []map[string]any{{
				"createdDateTime": map[string]string{"value": "2026-07-28T15:20:00Z"},
				"total":           "USD 16.98", "purchaseType": "PICKUP", "receiptKey": "trip-1",
				"lineItems": []map[string]string{{"upc": "0000000000105"}, {"upc": "0000000000106"}},
			}}}}})

		default:
			http.Error(w, "fakekroger: unexpected "+r.Method+" "+r.URL.Path, http.StatusMethodNotAllowed)
		}
	})
	log.Printf("fake Kroger on %s", base)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
