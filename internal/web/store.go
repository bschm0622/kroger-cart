package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Staple is one line of the staples file.
//
// Selected mirrors whether the line is commented out. The file keeps every item
// you have ever bought and marks the ones on the weekly order, rather than
// listing only the selected ones, so unchecking something in the UI doesn't
// lose the item or its purchase count.
type Staple struct {
	UPC      string `json:"upc"`
	Quantity int    `json:"qty"`
	Times    int    `json:"times"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

// Recipe is a named list of ingredients that can be added to the cart at once.
type Recipe struct {
	// Slug is the filename stem. It is derived from Name on create and then
	// never changes, so a rename can't orphan the file.
	Slug        string       `json:"slug" toml:"-"`
	Name        string       `json:"name" toml:"name"`
	Notes       string       `json:"notes,omitempty" toml:"notes,omitempty"`
	Ingredients []RecipeItem `json:"ingredients" toml:"ingredients"`
}

// RecipeItem is one ingredient. Name is stored alongside the UPC so a recipe
// file stays readable, and so the UI can render it without a live API call.
type RecipeItem struct {
	UPC      string `json:"upc" toml:"upc"`
	Quantity int    `json:"qty" toml:"qty"`
	Name     string `json:"name,omitempty" toml:"name,omitempty"`
}

// paths resolves where the UI's own files live. They sit next to config.toml
// rather than inside the SQLite store: the store is a cache of Kroger's data
// and gets re-synced, while these are the user's own lists and should stay
// hand-editable and greppable.
type paths struct {
	staples string
	recipes string
}

func newPaths(configDir string) paths {
	return paths{
		staples: filepath.Join(configDir, "staples.tsv"),
		recipes: filepath.Join(configDir, "recipes"),
	}
}

// staplesHeader is rewritten on every save. It documents the format in the
// file itself, because the file is meant to stay usable without the UI.
const staplesHeader = `# Kroger staples — the master list of things you buy.
#
# A commented line is off this week's order; an uncommented line is on it.
# The web UI (kroger-pp-cli serve) rewrites this file, and the CLI script
# scripts/kroger-weekly-cart reads it. Both accept hand edits.
#
#   qty<TAB>upc<TAB>times-bought<TAB>name
`

// LoadStaples reads the staples file. A missing file is not an error: it just
// means nothing has been seeded yet, and the UI offers to seed from history.
func LoadStaples(path string) ([]Staple, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Staple
	for i, line := range strings.Split(string(raw), "\n") {
		s, ok, err := parseStapleLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// commentedStaple matches a staples line that has been commented out. The
// leading "# " is the selection toggle, so it has to be stripped before the
// columns can be read — but a genuine prose comment must not be, or the header
// block would parse as items.
var commentedStaple = regexp.MustCompile(`^#\s*(\d+\t.*)$`)

func parseStapleLine(line string) (Staple, bool, error) {
	line = strings.TrimRight(line, "\r")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Staple{}, false, nil
	}
	selected := true
	if strings.HasPrefix(trimmed, "#") {
		m := commentedStaple.FindStringSubmatch(line)
		if m == nil {
			return Staple{}, false, nil // prose comment
		}
		line, selected = m[1], false
	}
	cols := strings.Split(line, "\t")
	if len(cols) < 2 {
		return Staple{}, false, fmt.Errorf("expected tab-separated 'qty<TAB>upc', got %q", trimmed)
	}
	qty, err := strconv.Atoi(strings.TrimSpace(cols[0]))
	if err != nil || qty < 0 {
		return Staple{}, false, fmt.Errorf("quantity must be a whole number, got %q", cols[0])
	}
	if qty == 0 {
		qty, selected = 1, false
	}
	s := Staple{UPC: strings.TrimSpace(cols[1]), Quantity: qty, Selected: selected}
	if len(cols) > 2 {
		s.Times, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(cols[2]), "x"))
	}
	if len(cols) > 3 {
		s.Name = strings.TrimSpace(cols[3])
	}
	return s, true, nil
}

// SaveStaples rewrites the staples file, most-bought first.
//
// The write goes through a temp file in the same directory and a rename, so an
// interrupted save can't leave a half-written list — this file is the only
// record of which items are staples.
func SaveStaples(path string, items []Staple) error {
	sorted := make([]Staple, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Times != sorted[j].Times {
			return sorted[i].Times > sorted[j].Times
		}
		return sorted[i].UPC < sorted[j].UPC
	})

	var b strings.Builder
	b.WriteString(staplesHeader)
	b.WriteString("\n")
	for _, s := range sorted {
		if s.UPC == "" {
			continue
		}
		qty := max(s.Quantity, 1)
		prefix := "# "
		if s.Selected {
			prefix = ""
		}
		name := strings.ReplaceAll(s.Name, "\t", " ")
		fmt.Fprintf(&b, "%s%d\t%s\t%dx\t%s\n", prefix, qty, s.UPC, s.Times, name)
	}
	return writeFileAtomic(path, []byte(b.String()))
}

// LoadRecipes reads every recipe file. A recipe that fails to parse is
// reported rather than skipped: silently dropping one would look like the
// recipe was deleted.
func LoadRecipes(dir string) ([]Recipe, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Recipe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var r Recipe
		if err := toml.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		r.Slug = strings.TrimSuffix(e.Name(), ".toml")
		if r.Name == "" {
			r.Name = r.Slug
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func SaveRecipe(dir string, r Recipe) (Recipe, error) {
	if strings.TrimSpace(r.Name) == "" {
		return r, fmt.Errorf("a recipe needs a name")
	}
	if r.Slug != "" && r.Slug != slugify(r.Slug) {
		return r, fmt.Errorf("invalid recipe id %q", r.Slug)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	if r.Slug == "" {
		base := slugify(r.Name)
		if base == "" {
			return r, fmt.Errorf("recipe name %q has no characters usable in a filename", r.Name)
		}
		r.Slug = base
		for n := 2; fileExists(filepath.Join(dir, r.Slug+".toml")); n++ {
			r.Slug = fmt.Sprintf("%s-%d", base, n)
		}
	}
	for i := range r.Ingredients {
		if r.Ingredients[i].Quantity < 1 {
			r.Ingredients[i].Quantity = 1
		}
	}
	raw, err := toml.Marshal(r)
	if err != nil {
		return r, err
	}
	return r, writeFileAtomic(filepath.Join(dir, r.Slug+".toml"), raw)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func DeleteRecipe(dir, slug string) error {
	if slug != slugify(slug) || slug == "" {
		// Reject anything that isn't already a clean slug so a crafted slug
		// can't escape the recipes directory.
		return fmt.Errorf("invalid recipe id %q", slug)
	}
	err := os.Remove(filepath.Join(dir, slug+".toml"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// writeFileAtomic writes via a temp file in the same directory, then renames.
// Same-directory matters: rename is only atomic within a filesystem.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
