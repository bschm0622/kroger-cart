package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/bschm0622/kroger-cart/internal/client"
	"github.com/bschm0622/kroger-cart/internal/web"
)

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
			return srv.ListenAndServe(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8787", "Address to listen on (localhost only)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Don't open a browser window")

	return cmd
}

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
