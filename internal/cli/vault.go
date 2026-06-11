package cli

import "github.com/spf13/cobra"

// newVaultCmd is the parent of the checkout-free vault operations that act on a
// storage location directly (no repo checkout). Subcommands attach here:
// coder-a owns `vault init` (Task 33); other vault subcommands (ls/stat/get/put/
// mv/rm/passwd/restore-identity) ADD themselves to this group's AddCommand list
// rather than re-declaring the group.
func newVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Checkout-free operations on a federated storage location",
	}
	cmd.AddCommand(
		newVaultInitCmd(),
		newVaultScanCmd(),
		newVaultStatCmd(),
		newVaultLsCmd(),
		newVaultRestoreIdentityCmd(),
		newVaultGetCmd(),
		newVaultPutCmd(),
		newVaultMvCmd(),
		newVaultRebuildCatalogCmd(),
	)
	return cmd
}
