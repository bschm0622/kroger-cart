package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaplesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staples.tsv")

	raw := staplesHeader + "\n" +
		"2\t0000000004011\t28x\tBananas\n" +
		"# 1\t0001111041660\t29x\tMilk\n" +
		"#1\t0001111008887\t7x\tChips\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := LoadStaples(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(items), items)
	}
	byUPC := map[string]Staple{}
	for _, i := range items {
		byUPC[i.UPC] = i
	}
	if b := byUPC["0000000004011"]; !b.Selected || b.Quantity != 2 || b.Times != 28 || b.Name != "Bananas" {
		t.Errorf("bananas parsed as %+v", b)
	}
	if m := byUPC["0001111041660"]; m.Selected {
		t.Error("a commented line must parse as unselected")
	}
	if c, ok := byUPC["0001111008887"]; !ok || c.Selected {
		t.Errorf("chips parsed as %+v (ok=%v)", c, ok)
	}

	if err := SaveStaples(path, items); err != nil {
		t.Fatal(err)
	}
	again, err := LoadStaples(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(items) {
		t.Fatalf("round trip changed the count: %d -> %d", len(items), len(again))
	}
	for _, a := range again {
		want := byUPC[a.UPC]
		if a.Selected != want.Selected || a.Quantity != want.Quantity || a.Times != want.Times || a.Name != want.Name {
			t.Errorf("round trip changed %s: %+v -> %+v", a.UPC, want, a)
		}
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# Kroger staples") {
		t.Error("saved file lost its explanatory header")
	}
}

func TestLoadStaplesMissingFileIsEmpty(t *testing.T) {
	items, err := LoadStaples(filepath.Join(t.TempDir(), "absent.tsv"))
	if err != nil {
		t.Fatalf("want no error for a missing file, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("want no items, got %d", len(items))
	}
}

func TestLoadStaplesRejectsBadQuantity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staples.tsv")
	if err := os.WriteFile(path, []byte("zero\t0000000004011\t1x\tBananas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStaples(path); err == nil {
		t.Fatal("want an error naming the bad line")
	}
}

func TestSaveStaplesNeutralizesTabsInNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staples.tsv")
	if err := SaveStaples(path, []Staple{{UPC: "1", Quantity: 1, Name: "Milk\t1 gal", Selected: true}}); err != nil {
		t.Fatal(err)
	}
	items, err := LoadStaples(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "Milk 1 gal" {
		t.Fatalf("got %+v", items)
	}
}

func TestRecipeRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipes")
	saved, err := SaveRecipe(dir, Recipe{
		Name:  "Turkey Tacos",
		Notes: "Tuesday",
		Ingredients: []RecipeItem{
			{UPC: "0001111066275", Quantity: 1, Name: "Ground turkey"},
			{UPC: "0001111062960", Name: "Onions"}, // quantity defaults to 1
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Slug != "turkey-tacos" {
		t.Fatalf("got slug %q, want turkey-tacos", saved.Slug)
	}
	if saved.Ingredients[1].Quantity != 1 {
		t.Errorf("a missing quantity should default to 1, got %d", saved.Ingredients[1].Quantity)
	}

	all, err := LoadRecipes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "Turkey Tacos" || len(all[0].Ingredients) != 2 {
		t.Fatalf("got %+v", all)
	}
	if all[0].Slug != "turkey-tacos" {
		t.Errorf("slug must come back from the filename, got %q", all[0].Slug)
	}

	if err := DeleteRecipe(dir, "turkey-tacos"); err != nil {
		t.Fatal(err)
	}
	if all, _ := LoadRecipes(dir); len(all) != 0 {
		t.Fatalf("want no recipes after delete, got %d", len(all))
	}
}

func TestSaveRecipeKeepsSlugOnRename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recipes")
	first, err := SaveRecipe(dir, Recipe{Name: "Tacos"})
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "Turkey Tacos"
	if _, err := SaveRecipe(dir, first); err != nil {
		t.Fatal(err)
	}
	all, err := LoadRecipes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("rename created %d files, want 1", len(all))
	}
	if all[0].Name != "Turkey Tacos" || all[0].Slug != "tacos" {
		t.Fatalf("got name=%q slug=%q", all[0].Name, all[0].Slug)
	}
}

func TestDeleteRecipeRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "keepme.toml")
	if err := os.WriteFile(victim, []byte("name='keep'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"../keepme", "..", "", "sub/keepme", "/etc/passwd"} {
		if err := DeleteRecipe(filepath.Join(dir, "recipes"), slug); err == nil {
			t.Errorf("slug %q was accepted", slug)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file outside the recipes dir was removed: %v", err)
	}
}

func TestRecipeNeedsAName(t *testing.T) {
	if _, err := SaveRecipe(t.TempDir(), Recipe{Name: "  "}); err == nil {
		t.Fatal("want an error for a blank name")
	}
	if _, err := SaveRecipe(t.TempDir(), Recipe{Name: "!!!"}); err == nil {
		t.Fatal("want an error for a name with no usable characters")
	}
}
