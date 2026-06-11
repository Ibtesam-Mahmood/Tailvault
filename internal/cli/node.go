package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// newNodeCmd is the hidden `tailvault node` group: helpers that run ON a storage
// node, invoked over SSH by the client. They are not part of the user-facing
// surface (Hidden), but must still be reachable as subcommands of the installed
// binary on the node.
func newNodeCmd() *cobra.Command {
	node := &cobra.Command{
		Use:    "node",
		Short:  "Node-side helpers invoked over SSH (internal)",
		Hidden: true,
	}
	node.AddCommand(newNodeVerifyPasswdCmd())
	return node
}

// newNodeVerifyPasswdCmd implements `tailvault node verify-passwd --vault <base>`.
// It runs on the node, reads the candidate password verbatim from stdin (the
// client writes exactly the password bytes over the SSH channel — no trailing
// newline), loads the local hash file, and exits 0 on a match. The stored hash
// NEVER leaves the node: only the exit status crosses back.
//
//   - match            -> nil (exit 0)
//   - no password set  -> TV-AUTH-01 (the node refuses; client tells the user to
//     run `tailvault vault passwd <location>`)
//   - rejected         -> TV-AUTH-01
//   - unreadable hash  -> TV-AUTH-01 (cannot authenticate; never a false accept)
//
// All non-match outcomes map to exit bucket 2, so any non-zero exit the client
// sees over SSH means "not authorized" — it never falls back to client-side
// verification (which would require shipping the hash off the node).
func newNodeVerifyPasswdCmd() *cobra.Command {
	var vault string
	c := &cobra.Command{
		Use:    "verify-passwd",
		Short:  "Verify a password against this node's hash file (reads stdin)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if vault == "" {
				return tserr.ConfigErr("node verify-passwd: --vault <base_path> is required", nil)
			}
			candidate, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return tserr.ConfigErr("node verify-passwd: read candidate from stdin", err)
			}
			defer auth.Zero(candidate)

			hf, ok, err := auth.LoadHashFile(auth.HashFilePath(vault))
			if err != nil {
				// Corrupt/unreadable hash file: cannot authenticate. Refuse, never
				// guess. Exit bucket 2.
				return tserr.AuthErr("node password hash file is unreadable", err)
			}
			if !ok {
				return tserr.AuthErr("no vault password set on node", auth.ErrNoPassword)
			}
			if !auth.Verify(hf, candidate) {
				return tserr.AuthErr("password rejected", auth.ErrWrongPassword)
			}
			return nil
		},
	}
	c.Flags().StringVar(&vault, "vault", "", "node base_path of the vault (joined with meta/auth/passwd)")
	return c
}
