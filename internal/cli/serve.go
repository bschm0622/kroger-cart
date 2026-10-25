// Copyright 2026 bschm0622 and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/bschm0622/kroger-cart/internal/client"
	"github.com/bschm0622/kroger-cart/internal/web"
)

// newServeCmd starts the local web UI.
//
// The UI exists because the weekly grocery order is a clicking task, not a
// typing one: picking this week's staples out of a hundred-item list and
// bulk-adding a recipe's ingredients are both painful as flags and obvious as
// checkboxes. It shares the CLI's config and client, so there is one session to
// keep alive rather than two.
func newServeCmd(flags *rootFlags) *cobra.Command {
	var addr string
	var noOpen bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Open a local web UI for staples, recipes, and cart history",
		Long: "Open a local web UI for staples, recipes, and cart history.\n\n" +
			"Serves on localhost only. The saved Kroger session can read your order\n" +
			"history and place orders, and this server has no authentication of its\n" +
			"own, so do not put it on a network or behind a tunnel.\n\n" +
			"The UI can also take a freshly pasted Cookie header, which means the\n" +
			"weekly session refresh no longer needs the terminal.",
		Example: "  kroger-pp-cli serve\n  kroger-pp-cli serve --addr 127.0.0.1:9000 --no-open",
		Annotations: map[string]string{
			// Not an API endpoint, and a long-running server is not a useful
			// MCP tool — an agent would block on it forever.
			"mcp:skip": "true",
		},
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			newClient := func() (*client.Client, error) { return flags.newClient() }
			srv, err := web.NewServer(addr, flags.configPath, newClient)
			if err != nil {
				return configErr(err)
			}
			url := "http://" + addr
			fmt.Fprintf(cmd.OutOrStdout(), "Kroger cart UI on %s\nPress Ctrl-C to stop.\n", url)
			if !noOpen {
				openBrowser(url)
			}
			// cobra wires SIGINT into the command context, so Ctrl-C shuts the
			// listener down rather than killing the process mid-write to the
			// staples file.
			return srv.ListenAndServe(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8787", "Address to listen on (localhost only)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Don't open a browser window")

	return cmd
}

// openBrowser is best-effort: a failure here costs the user one click, so it is
// deliberately not an error.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
