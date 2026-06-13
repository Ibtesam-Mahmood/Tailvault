package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/appconfig"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// newConfigCmd builds `tailvault config`: a quick doctor/fix for tailscale CLI
// resolution. With no flags it locates the tailscale binary (PATH → OS
// well-known locations), persists the path to ~/.config/tailvault/config.toml so
// every later command finds it, and reports tailnet health. It hard-errors with
// an install hint if no binary is found. `--show` just prints current state.
func newConfigCmd() *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Locate and register the tailscale CLI (fixes peer-discovery on GUI-app machines)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			if show {
				cfg, err := appconfig.Load()
				if err != nil {
					return err
				}
				if cfg.TailscalePath == "" {
					fmt.Fprintln(out, "tailscale_path: (unset — resolving from PATH / well-known locations)")
				} else {
					fmt.Fprintf(out, "tailscale_path: %s\n", cfg.TailscalePath)
				}
				if p, ok := tailscale.Locate(); ok {
					fmt.Fprintf(out, "resolves to:    %s\n", p)
				} else {
					fmt.Fprintln(out, "resolves to:    (not found)")
				}
				return nil
			}

			// Locate the binary; not finding it is a hard, actionable error.
			path, ok := tailscale.Locate()
			if !ok {
				return tserr.NetBinaryNotFoundErr(nil)
			}

			// Persist the resolved path so future runs (and peer discovery) find it.
			cfg, err := appconfig.Load()
			if err != nil {
				return err
			}
			cfg.TailscalePath = path
			if err := cfg.Save(); err != nil {
				return err
			}
			cfgPath, _ := appconfig.Path()
			fmt.Fprintf(out, "tailscale CLI: %s\n", path)
			fmt.Fprintf(out, "saved to:      %s\n", cfgPath)

			// Doctor: try the local session. A down/logged-out daemon is reported
			// as a soft note — locating + saving the binary already succeeded.
			st, serr := tailscale.New().Status(cmd.Context())
			if serr != nil {
				fmt.Fprintf(out, "tailnet:       not ready — %v\n", serr)
				return nil
			}
			peers, _ := setup.OnlinePeers(st, nil)
			fmt.Fprintf(out, "tailnet:       logged in, %d peer(s) online\n", len(peers))
			return nil
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "print the stored tailscale path and what it resolves to, without changing anything")
	return cmd
}
