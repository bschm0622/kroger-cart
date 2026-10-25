// Package web serves a local single-page UI over the same authenticated
// client the CLI uses, so the weekly grocery order can be assembled by
// clicking rather than by hand-editing a TSV file.
//
// It binds to localhost only and has no authentication of its own: the saved
// Kroger session is bearer-equivalent, so exposing this port to a network
// would hand over order history, saved addresses, and the ability to place
// orders. See Server.Addr.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bschm0622/kroger-cart/internal/client"
)

// Item is a product as the UI needs it: enough to show a row and to build a
// cart line item from it.
type Item struct {
	UPC   string `json:"upc"`
	Name  string `json:"name"`
	Size  string `json:"size,omitempty"`
	Image string `json:"image,omitempty"`
	Price string `json:"price,omitempty"`

	// DeptCode drives Orderable; it is not shown, so it stays off the wire.
	DeptCode string `json:"-"`
}

// CartLine is one line currently in the Kroger cart. ID is server-assigned and
// required to change or remove the line.
type CartLine struct {
	ID       string `json:"id"`
	UPC      string `json:"upc"`
	Quantity int    `json:"quantity"`
	Modality string `json:"modality,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Trip is one past purchase.
type Trip struct {
	Date       string   `json:"date"`
	Total      string   `json:"total"`
	Type       string   `json:"type"`
	ReceiptKey string   `json:"receiptKey"`
	UPCs       []string `json:"upcs"`
}

// api wraps the CLI's client with the response-shape knowledge the UI needs.
// Every method returns a plain Go value so handlers stay free of JSON pathing.
type api struct {
	c *client.Client
}

const (
	pathCarts    = "/atlas/v1/carts"
	pathCart     = "/atlas/v1/carts/"
	pathProducts = "/atlas/v1/product/v2/products"
	pathSearch   = "/atlas/v1/search/v1/products-search"
	pathHistory  = "/atlas/v1/post-order/v1/purchase-history-search"

	// productProjections mirrors the CLI's default. items.full carries the
	// description and size the UI shows; offers.compact carries price.
	productProjections = "items.full,offers.compact,nutrition.label,inventory.projected"
)

// Cart returns the active cart's id and its current lines.
//
// An account can hold a saved-for-later cart alongside the live one, so the
// ACTIVE type is matched rather than the first entry.
func (a *api) Cart(ctx context.Context) (string, []CartLine, error) {
	raw, err := a.c.GetNoCache(ctx, pathCarts, nil)
	if err != nil {
		return "", nil, err
	}
	var body struct {
		Data struct {
			Carts []struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				LineItems []struct {
					ID           string `json:"id"`
					GTIN13       string `json:"gtin13"`
					Quantity     int    `json:"quantity"`
					ModalityType string `json:"modalityType"`
				} `json:"lineItems"`
			} `json:"carts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", nil, fmt.Errorf("parsing carts response: %w", err)
	}
	for _, c := range body.Data.Carts {
		if c.Type != "ACTIVE" {
			continue
		}
		lines := make([]CartLine, 0, len(c.LineItems))
		for _, li := range c.LineItems {
			lines = append(lines, CartLine{
				ID:       li.ID,
				UPC:      li.GTIN13,
				Quantity: li.Quantity,
				Modality: li.ModalityType,
			})
		}
		return c.ID, lines, nil
	}
	if len(body.Data.Carts) == 0 {
		return "", nil, fmt.Errorf("no carts on this account; add one item on kroger.com to create one")
	}
	return "", nil, fmt.Errorf("found %d cart(s) but none are ACTIVE; open kroger.com to create a shopping cart", len(body.Data.Carts))
}

// wantLine is a requested quantity for a UPC.
type wantLine struct {
	UPC      string `json:"upc"`
	Quantity int    `json:"qty"`
}

// AddToCart sets the quantity of each requested UPC in the cart.
//
// The API sets rather than increments, so adding the same list twice is a
// no-op rather than a double order — which is what makes the weekly button
// safe to press again when you're not sure whether it went through.
func (a *api) AddToCart(ctx context.Context, cartID string, want []wantLine, modality string) error {
	if len(want) == 0 {
		return fmt.Errorf("nothing selected")
	}
	items := make([]map[string]any, 0, len(want))
	for _, w := range want {
		if w.Quantity < 1 {
			return fmt.Errorf("quantity for %s must be at least 1", w.UPC)
		}
		items = append(items, lineItem(w.UPC, w.Quantity, modality, ""))
	}
	_, _, err := a.c.Put(ctx, pathCart+cartID, map[string]any{"lineItems": items})
	return err
}

// ClearCart empties the cart.
//
// Removal is quantity 0 against the server-assigned line item id; an empty
// lineItems array is rejected with "must contain at least 1 items", so an
// empty cart has to be spelled out one line at a time.
func (a *api) ClearCart(ctx context.Context, cartID string, lines []CartLine) error {
	if len(lines) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(lines))
	for _, li := range lines {
		items = append(items, lineItem(li.UPC, 0, li.Modality, li.ID))
	}
	_, _, err := a.c.Put(ctx, pathCart+cartID, map[string]any{"lineItems": items})
	return err
}

// lineEdit is a new quantity for a line already in the cart; 0 removes it.
type lineEdit struct {
	Line     CartLine
	Quantity int
}

// EditLines changes lines already in the cart, addressed by their server id,
// in one write. New products go through AddToCart instead: a fresh add must
// not carry an id.
func (a *api) EditLines(ctx context.Context, cartID string, edits []lineEdit) error {
	if len(edits) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(edits))
	for _, e := range edits {
		items = append(items, lineItem(e.Line.UPC, e.Quantity, e.Line.Modality, e.Line.ID))
	}
	_, _, err := a.c.Put(ctx, pathCart+cartID, map[string]any{"lineItems": items})
	return err
}

// SetQuantity changes one existing line, or removes it when qty is 0.
func (a *api) SetQuantity(ctx context.Context, cartID string, line CartLine, qty int) error {
	item := lineItem(line.UPC, qty, line.Modality, line.ID)
	_, _, err := a.c.Put(ctx, pathCart+cartID, map[string]any{"lineItems": []map[string]any{item}})
	return err
}

func lineItem(upc string, qty int, modality, id string) map[string]any {
	if modality == "" {
		modality = "PICKUP"
	}
	item := map[string]any{
		"gtin13":             upc,
		"quantity":           qty,
		"modalityType":       modality,
		"channel":            "WEB",
		"substitutionPolicy": "SHOPPER_CHOICE",
		"savedForLater":      false,
	}
	// The id is only present when editing a line that already exists; sending
	// an empty one on a fresh add is rejected.
	if id != "" {
		item["id"] = id
	}
	return item
}

// Products resolves UPCs to names in bulk.
//
// The UPC list is sent as a repeated query key. Comma-joining it draws an
// Akamai "Access Denied" HTML interstitial instead of a result, and batches are
// capped because the endpoint rejects an unbounded list.
//
// Anything the verified catalog does not carry is retried unverified. Real
// groceries land there — deli-counter and other weighed items are absent from
// the verified catalog, and a history tab full of bare barcodes is the result of
// taking the first miss as final.
func (a *api) Products(ctx context.Context, upcs []string) (map[string]Item, error) {
	out := make(map[string]Item, len(upcs))
	if err := a.products(ctx, upcs, true, out); err != nil {
		return out, err
	}
	var missing []string
	for _, u := range upcs {
		if _, ok := out[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		if err := a.products(ctx, missing, false, out); err != nil {
			// The verified pass already succeeded, so return what it found
			// rather than failing the whole lookup over the fallback.
			return out, nil
		}
	}
	return out, nil
}

func (a *api) products(ctx context.Context, upcs []string, verified bool, out map[string]Item) error {
	const batch = 24
	for start := 0; start < len(upcs); start += batch {
		end := min(start+batch, len(upcs))
		raw, err := a.c.GetNoCache(ctx, pathProducts, map[string]string{
			"filter.gtin13s":  client.MultiValue(upcs[start:end]),
			"filter.verified": strconv.FormatBool(verified),
			"projections":     productProjections,
		})
		if err != nil {
			return err
		}
		var body struct {
			Data struct {
				Products []struct {
					Item struct {
						UPC                string `json:"upc"`
						Description        string `json:"description"`
						CustomerFacingSize string `json:"customerFacingSize"`
						FamilyTree         struct {
							Department struct {
								Code string `json:"code"`
								Name string `json:"name"`
							} `json:"department"`
						} `json:"familyTree"`
						Images []struct {
							Size string `json:"size"`
							URL  string `json:"url"`
						} `json:"images"`
					} `json:"item"`
				} `json:"products"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("parsing products response: %w", err)
		}
		for _, p := range body.Data.Products {
			if p.Item.UPC == "" {
				continue
			}
			it := Item{
				UPC:      p.Item.UPC,
				Name:     p.Item.Description,
				Size:     p.Item.CustomerFacingSize,
				DeptCode: p.Item.FamilyTree.Department.Code,
			}
			for _, img := range p.Item.Images {
				if img.Size == "medium" || (it.Image == "" && img.Size == "small") {
					it.Image = img.URL
				}
			}
			out[it.UPC] = it
		}
	}
	return nil
}

// nonGroceryDepts are departments whose "products" are transaction lines, not
// things you can put in a cart: the pickup fee that rides along on every pickup
// order, and the gas pump. Left in, the fee outranks every actual grocery,
// because it appears on more trips than anything you eat.
var nonGroceryDepts = map[string]string{
	"34": "in-store pickup fee",
	"58": "fuel",
}

// Orderable reports whether an item belongs on a shopping list at all.
func (i Item) Orderable() bool {
	_, blocked := nonGroceryDepts[i.DeptCode]
	return !blocked
}

// Search runs a keyword product search.
//
// The wire parameter is filter.query. filter.keyword is silently ignored
// rather than rejected, which returns a populated-looking envelope with zero
// products.
func (a *api) Search(ctx context.Context, keyword string, size int) ([]Item, error) {
	if size <= 0 {
		size = 24
	}
	raw, err := a.c.GetNoCache(ctx, pathSearch, map[string]string{
		"filter.query":              keyword,
		"filter.fulfillmentMethods": "PICKUP",
		"page.size":                 fmt.Sprint(size),
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Data struct {
			ProductsSearch []struct {
				UPC         string `json:"upc"`
				Description string `json:"description"`
				BrandName   string `json:"brandName"`
			} `json:"productsSearch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parsing search response: %w", err)
	}
	items := make([]Item, 0, len(body.Data.ProductsSearch))
	for _, p := range body.Data.ProductsSearch {
		if p.UPC == "" {
			continue
		}
		items = append(items, Item{UPC: p.UPC, Name: p.Description})
	}
	return items, nil
}

// History returns past trips, newest first.
func (a *api) History(ctx context.Context, size int) ([]Trip, error) {
	if size <= 0 {
		size = 25
	}
	// A GET, despite the "-search" suffix. Posting here returns 405.
	raw, err := a.c.GetNoCache(ctx, pathHistory, map[string]string{
		"pageNo":   "1",
		"pageSize": fmt.Sprint(size),
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Data struct {
			PostOrderSearch struct {
				Data []struct {
					CreatedDateTime struct {
						Value string `json:"value"`
					} `json:"createdDateTime"`
					Total        string `json:"total"`
					PurchaseType string `json:"purchaseType"`
					ReceiptKey   string `json:"receiptKey"`
					LineItems    []struct {
						UPC string `json:"upc"`
					} `json:"lineItems"`
				} `json:"data"`
			} `json:"postOrderSearch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parsing purchase history response: %w", err)
	}
	trips := make([]Trip, 0, len(body.Data.PostOrderSearch.Data))
	for _, t := range body.Data.PostOrderSearch.Data {
		upcs := make([]string, 0, len(t.LineItems))
		for _, li := range t.LineItems {
			if li.UPC != "" {
				upcs = append(upcs, li.UPC)
			}
		}
		date, _, _ := strings.Cut(t.CreatedDateTime.Value, "T")
		trips = append(trips, Trip{
			Date:       date,
			Total:      strings.TrimPrefix(t.Total, "USD "),
			Type:       t.PurchaseType,
			ReceiptKey: t.ReceiptKey,
			UPCs:       upcs,
		})
	}
	return trips, nil
}

// FrequentUPCs counts how many trips each UPC appears in, most frequent first.
// This is the same ranking the kroger-staples script produces, computed live so
// the UI's suggestions don't go stale.
func (a *api) FrequentUPCs(ctx context.Context, trips []Trip) []struct {
	UPC   string `json:"upc"`
	Times int    `json:"times"`
} {
	counts := map[string]int{}
	for _, t := range trips {
		seen := map[string]bool{}
		for _, u := range t.UPCs {
			// A UPC listed twice on one receipt is still one trip.
			if seen[u] {
				continue
			}
			seen[u] = true
			counts[u]++
		}
	}
	type row = struct {
		UPC   string `json:"upc"`
		Times int    `json:"times"`
	}
	out := make([]row, 0, len(counts))
	for u, n := range counts {
		out = append(out, row{UPC: u, Times: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Times != out[j].Times {
			return out[i].Times > out[j].Times
		}
		return out[i].UPC < out[j].UPC
	})
	return out
}
