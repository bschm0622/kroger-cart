// Copyright 2026 bschm0622 and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newCartGetCmd exposes GET /atlas/v1/carts.
//
// Every write to the cart is a PUT to /atlas/v1/carts/<cart_id>, so a cart id
// is required to do anything at all — but the research capture only contained
// writes to an id the page already held, so no read command was generated and
// the id had no in-CLI source. `cart add` was therefore unusable without
// hand-copying a UUID out of DevTools, which also makes it unschedulable.
func newCartGetCmd(flags *rootFlags) *cobra.Command {
	var idOnly bool

	cmd := &cobra.Command{
		Use:         "get",
		Short:       "List your carts (use --id-only to get the active cart id for 'cart add')",
		Example:     "  kroger-pp-cli cart get\n  kroger-pp-cli cart get --id-only",
		Annotations: map[string]string{"pp:endpoint": "cart.get", "pp:method": "GET", "pp:path": "/atlas/v1/carts", "mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := flags.newClient()
			if err != nil {
				return err
			}

			path := "/atlas/v1/carts"
			data, prov, err := resolveReadWithStrategy(cmd.Context(), c, flags, "auto", "cart", false, path, map[string]string{}, nil, cmd.ErrOrStderr())
			if err != nil {
				return classifyAPIError(err, flags)
			}

			// --id-only prints the bare active cart id so it can be
			// substituted straight into `cart add`, which is the whole reason
			// this command exists. Anything but a lone id on stdout would have
			// to be parsed back out by the caller.
			if idOnly {
				id, err := activeCartID(data)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), id)
				return nil
			}

			if flags.asJSON || (!isTerminal(cmd.OutOrStdout()) && !flags.csv && !flags.quiet && !flags.plain) {
				filtered := data
				if flags.selectFields != "" {
					filtered = filterFields(filtered, flags.selectFields)
				} else if flags.compact {
					filtered = compactFields(filtered)
				}
				wrapped, wrapErr := wrapWithProvenance(filtered, prov)
				if wrapErr != nil {
					return wrapErr
				}
				return printOutput(cmd.OutOrStdout(), wrapped, true)
			}
			return printOutputWithFlags(cmd.OutOrStdout(), data, flags)
		},
	}
	cmd.Flags().BoolVar(&idOnly, "id-only", false, "Print just the active cart id, for piping into 'cart add'")

	return cmd
}

// activeCartID pulls the ACTIVE cart's id out of a GET /atlas/v1/carts body.
//
// An account can hold more than one cart (a list saved for later shows up
// alongside the live one), so picking carts[0] would intermittently write to
// the wrong cart. Only type == "ACTIVE" is the shopping cart. The type field
// is trusted over ordering because the API does not document a sort.
func activeCartID(data []byte) (string, error) {
	var body struct {
		Data struct {
			Carts []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"carts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("parsing carts response: %w", err)
	}
	if len(body.Data.Carts) == 0 {
		return "", fmt.Errorf("no carts found on this account; open kroger.com and add an item to create one")
	}
	for _, c := range body.Data.Carts {
		if c.Type == "ACTIVE" {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("found %d cart(s) but none are ACTIVE; open kroger.com to create a shopping cart", len(body.Data.Carts))
}
