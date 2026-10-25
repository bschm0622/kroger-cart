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

type Staple struct {
	UPC      string `json:"upc"`
	Quantity int    `json:"qty"`
	Times    int    `json:"times"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

type Recipe struct {
	Slug        string       `json:"slug" toml:"-"`
	Name        string       `json:"name" toml:"name"`
	Notes       string       `json:"notes,omitempty" toml:"notes,omitempty"`
	Ingredients []RecipeItem `json:"ingredients" toml:"ingredients"`
}

type RecipeItem struct {
	UPC      string `json:"upc" toml:"upc"`
	Quantity int    `json:"qty" toml:"qty"`
	Name     string `json:"name,omitempty" toml:"name,omitempty"`
}

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

const staplesHeader = `# Kroger staples — the master list of things you buy.
#
# A commented line is off this week's order; an uncommented line is on it.
# The web UI (kroger-pp-cli serve) rewrites this file, and the CLI script
# scripts/kroger-weekly-cart reads it. Both accept hand edits.
#
#   qty<TAB>upc<TAB>times-bought<TAB>name
`

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

// A "#" before a staples line means "not in your usual"; other "#" lines are prose.
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
