package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bschm0622/kroger-cart/internal/client"
)

type Item struct {
	UPC   string `json:"upc"`
	Name  string `json:"name"`
	Size  string `json:"size,omitempty"`
	Image string `json:"image,omitempty"`
	Price string `json:"price,omitempty"`

	Stock       string `json:"stock,omitempty"`
	WasPrice    string `json:"wasPrice,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
	MaxQty      int    `json:"maxQty,omitempty"`

	DeptCode string `json:"-"`
}

type CartLine struct {
	ID          string `json:"id"`
	UPC         string `json:"upc"`
	Quantity    int    `json:"quantity"`
	Modality    string `json:"modality,omitempty"`
	Name        string `json:"name,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

type Trip struct {
	Date       string         `json:"date"`
	Total      string         `json:"total"`
	Type       string         `json:"type"`
	ReceiptKey string         `json:"receiptKey"`
	UPCs       []string       `json:"upcs"`
	Counts     map[string]int `json:"counts"`
}

type api struct {
	c *client.Client
}

func (a *api) modality() string { return client.ModalityType(a.c.Config) }

const (
	pathCarts    = "/atlas/v1/carts"
	pathCart     = "/atlas/v1/carts/"
	pathProducts = "/atlas/v1/product/v2/products"
	pathSearch   = "/atlas/v1/search/v1/products-search"
	pathHistory  = "/atlas/v1/post-order/v1/purchase-history-search"

	productProjections = "items.full,offers.compact,nutrition.label,inventory.projected"
)

var errNoActiveCart = errors.New("this Kroger account has no active cart yet")

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
	return "", nil, errNoActiveCart
}

type wantLine struct {
	UPC      string `json:"upc"`
	Quantity int    `json:"qty"`
}

// AddToCart sets quantities rather than adding to them, so sending a list twice is harmless.
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

type lineEdit struct {
	Line     CartLine
	Quantity int
}

// EditLines edits lines already in the cart by id; quantity 0 removes one. An empty lineItems array is rejected.
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

// A fresh add must not carry an id; Kroger rejects an empty one.
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
	if id != "" {
		item["id"] = id
	}
	return item
}

// Products looks UPCs up in both catalogs: deli and other weighed items are only in the unverified one.
// unchecked lists UPCs never answered for (a failed request); they are unknown, not missing.
func (a *api) Products(ctx context.Context, upcs []string) (found map[string]Item, unchecked []string, err error) {
	found = make(map[string]Item, len(upcs))
	if failed, err := a.products(ctx, upcs, true, found); err != nil {
		return found, failed, err
	}
	var missing []string
	for _, u := range upcs {
		if _, ok := found[u]; !ok {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		if failed, err := a.products(ctx, missing, false, found); err != nil {
			return found, failed, err
		}
	}
	return found, nil, nil
}

func (a *api) products(ctx context.Context, upcs []string, verified bool, out map[string]Item) ([]string, error) {
	const batch = 24
	mod := a.modality()
	for start := 0; start < len(upcs); start += batch {
		end := min(start+batch, len(upcs))
		raw, err := a.c.GetNoCache(ctx, pathProducts, map[string]string{
			// Repeated keys, not comma-joined: Akamai answers a comma list with an Access Denied page.
			"filter.gtin13s":  client.MultiValue(upcs[start:end]),
			"filter.verified": strconv.FormatBool(verified),
			"projections":     productProjections,
		})
		if err != nil {
			return upcs[start:], err
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
					// This, not modalityAvailabilities, says an item is out of stock: the latter still says "available".
					FulfillmentSummaries []struct {
						Type    string `json:"type"`
						Regular struct {
							Price string `json:"price"`
						} `json:"regular"`
						Sale *struct {
							Price string `json:"price"`
						} `json:"sale"`
						Availability struct {
							Sellable              bool   `json:"sellable"`
							InventoryLevel        string `json:"inventoryLevel"`
							UnavailabilityMessage string `json:"unavailabilityMessage"`
						} `json:"availability"`
						MaxOrderQuantity int `json:"maxOrderQuantity"`
					} `json:"fulfillmentSummaries"`
				} `json:"products"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return upcs[start:], fmt.Errorf("parsing products response: %w", err)
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
			if len(p.FulfillmentSummaries) > 0 {
				it.Unavailable = "Not available for " + strings.ToLower(mod)
				for _, f := range p.FulfillmentSummaries {
					if f.Type != mod {
						continue
					}
					it.Price = dollars(f.Regular.Price)
					if f.Sale != nil && f.Sale.Price != "" && f.Sale.Price != f.Regular.Price {
						it.Price = dollars(f.Sale.Price)
						it.WasPrice = dollars(f.Regular.Price)
					}
					it.Stock = f.Availability.InventoryLevel
					it.MaxQty = f.MaxOrderQuantity
					it.Unavailable = ""
					if !f.Availability.Sellable {
						it.Unavailable = f.Availability.UnavailabilityMessage
						if it.Unavailable == "" {
							it.Unavailable = "Not available right now"
						}
					}
				}
			}
			out[it.UPC] = it
		}
	}
	return nil, nil
}

func dollars(p string) string {
	if amt, ok := strings.CutPrefix(strings.TrimSpace(p), "USD "); ok {
		return "$" + amt
	}
	return p
}

// Transaction lines that appear on receipts but aren't groceries.
var nonGroceryDepts = map[string]string{
	"34": "in-store pickup fee",
	"58": "fuel",
}

func (i Item) Orderable() bool {
	_, blocked := nonGroceryDepts[i.DeptCode]
	return !blocked
}

func (a *api) Search(ctx context.Context, keyword string, size int) ([]Item, error) {
	if size <= 0 {
		size = 24
	}
	// The parameter is filter.query; filter.keyword is silently ignored.
	raw, err := a.c.GetNoCache(ctx, pathSearch, map[string]string{
		"filter.query":              keyword,
		"filter.fulfillmentMethods": a.modality(),
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
	upcs := make([]string, len(items))
	for i, it := range items {
		upcs[i] = it.UPC
	}
	details, _, _ := a.Products(ctx, upcs)
	for i, it := range items {
		if d, ok := details[it.UPC]; ok {
			items[i] = d
		}
	}
	return items, nil
}

func (a *api) History(ctx context.Context, size int) ([]Trip, error) {
	if size <= 0 {
		size = 25
	}
	// A GET despite the "-search" name; POST returns 405.
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
		counts := map[string]int{}
		for _, li := range t.LineItems {
			if li.UPC == "" {
				continue
			}
			if counts[li.UPC] == 0 {
				upcs = append(upcs, li.UPC)
			}
			counts[li.UPC]++
		}
		date, _, _ := strings.Cut(t.CreatedDateTime.Value, "T")
		trips = append(trips, Trip{
			Date:       date,
			Total:      strings.TrimPrefix(t.Total, "USD "),
			Type:       t.PurchaseType,
			ReceiptKey: t.ReceiptKey,
			UPCs:       upcs,
			Counts:     counts,
		})
	}
	return trips, nil
}

func frequentUPCs(trips []Trip) []struct {
	UPC   string `json:"upc"`
	Times int    `json:"times"`
} {
	counts := map[string]int{}
	for _, t := range trips {
		for _, u := range t.UPCs { // already one entry per product per trip
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
