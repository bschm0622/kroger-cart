package cli

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bschm0622/kroger-cart/internal/config"
)

var storeHeaders = []string{"x-laf-object", "x-modality", "x-geo-location-v1", "x-facility-id"}

var curlHeader = regexp.MustCompile(`(?s)(?:-H|--header)\s+(?:\$'((?:[^'\\]|\\.)*)'|'([^']*)'|"((?:[^"\\]|\\.)*)")`)

func parseStoreHeaders(raw string) map[string]string {
	var lines []string
	if m := curlHeader.FindAllStringSubmatch(raw, -1); len(m) > 0 {
		for _, g := range m {
			v := g[1] + g[2] + g[3]
			if g[1] != "" {
				v = strings.NewReplacer(`\'`, `'`, `\\`, `\`, `\"`, `"`).Replace(v)
			}
			lines = append(lines, v)
		}
	} else {
		lines = strings.Split(raw, "\n")
	}
	want := map[string]bool{}
	for _, h := range storeHeaders {
		want[h] = true
	}
	out := map[string]string{}
	for _, line := range lines {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if want[name] {
			out[name] = strings.TrimSpace(value)
		}
	}
	return out
}

func newAuthSetStoreCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "set-store -",
		Short:   "Save which Kroger store you shop, from a request copied out of DevTools",
		Example: "  pbpaste | kroger-pp-cli auth set-store -",
		Long: "Save which Kroger store you shop, from a request copied out of DevTools.\n\n" +
			"On kroger.com, signed in, search for anything. Open DevTools (⌥⌘I) →\n" +
			"Network, type products-search in the filter box, right-click the request →\n" +
			"Copy → Copy as cURL. Then:\n\n" +
			"    pbpaste | kroger-pp-cli auth set-store -\n\n" +
			"Only the store headers (x-laf-object, x-modality, x-geo-location-v1 and,\n" +
			"if present, x-facility-id) are kept; the rest of the paste, including its\n" +
			"cookies, is ignored. Values are never printed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := args[0]
			if raw == "-" {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return configErr(fmt.Errorf("reading the pasted request from stdin: %w", err))
				}
				raw = string(b)
			}
			found := parseStoreHeaders(raw)
			if found["x-laf-object"] == "" {
				return configErr(fmt.Errorf("no x-laf-object header in what was pasted. " +
					"Copy a products-search request from DevTools → Network (right-click → Copy → Copy as cURL), " +
					"after searching for something on kroger.com"))
			}
			cfg, err := config.Load(flags.configPath)
			if err != nil {
				return configErr(err)
			}
			if err := cfg.ReplaceHeaders(storeHeaders, found); err != nil {
				return configErr(fmt.Errorf("saving store headers: %w", err))
			}
			names := make([]string, 0, len(found))
			for k := range found {
				names = append(names, k)
			}
			sort.Strings(names)
			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"saved":       names,
					"config_path": cfg.Path,
				}, flags)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s Saved your store (%s) to %s\n", green("OK"), strings.Join(names, ", "), cfg.Path)
			return nil
		},
	}
}
