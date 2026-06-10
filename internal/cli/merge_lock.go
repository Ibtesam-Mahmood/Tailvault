package cli

import (
	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

// newMergeLockCmd is the hidden git merge driver for tailvault.lock, registered
// by init as `merge.tailvault.driver = "tailvault __merge-lock %O %A %B"`. git
// invokes it with the base (%O), ours (%A, also the output file git reads back)
// and theirs (%B). It writes the canonical per-path union merge into %A and
// exits 0; a non-zero exit would leave the conflict for the user.
func newMergeLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__merge-lock <base> <ours> <theirs>",
		Short:  "Internal git merge driver for tailvault.lock (per-path union)",
		Hidden: true,
		Args:   cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			base := loadOrEmpty(args[0]) // base may be absent in an add/add merge
			ours, err := lock.Load(args[1])
			if err != nil {
				return tserr.ConfigErr("merge driver: parse ours lock "+args[1], err)
			}
			theirs, err := lock.Load(args[2])
			if err != nil {
				return tserr.ConfigErr("merge driver: parse theirs lock "+args[2], err)
			}
			merged, err := lock.Merge(base, ours, theirs)
			if err != nil {
				return err // non-zero => git records a conflict for the user
			}
			return lock.Write(args[1], merged, "tailvault "+version.Version)
		},
	}
}

// loadOrEmpty returns the parsed lock at path, or an empty Lock if it is missing
// or unparseable. The merge base legitimately may not exist (add/add), and the
// union rule does not require it, so a missing base must not fail the merge.
func loadOrEmpty(path string) *lock.Lock {
	l, err := lock.Load(path)
	if err != nil {
		return &lock.Lock{}
	}
	return l
}
