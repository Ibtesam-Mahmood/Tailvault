package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// gatedAnnotation marks a cobra command as a password-gated mutating remote op
// (§16). The enforcement audit (auth_enforcement_test.go) walks the command tree
// asserting exactly the SPEC gated set carries it and no read command does — so a
// new mutating command added without a gate, or a read accidentally gated, is a
// test failure, not a silent regression.
const gatedAnnotation = "tailvault.gated"

// markGated tags a command as password-gated for the enforcement audit. Call it
// on every command whose RunE calls gateLocation before its WAL intent.
func markGated(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[gatedAnnotation] = "1"
	return cmd
}

// newVaultPasswdCmd implements `tailvault vault passwd <location>`: set or change
// the node's per-node password (D9 / SPEC v2 §16). The secret is an argon2id PHC
// hash written to the node at meta/auth/passwd; it is itself a mutating, WAL-logged
// op. A CHANGE is gated on the OLD password first; a first SET is not (none exists
// yet). There is NO recovery flow — a forgotten password is reset only by SSH /
// physical access to the node (delete meta/auth/passwd).
func newVaultPasswdCmd() *cobra.Command {
	var oldFile, newFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "passwd <location>",
		Short: "Set or change a node's per-node password (no recovery)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultPasswd(cmd, args[0], passwdFlags{oldFile: oldFile, newFile: newFile, json: jsonOut})
		},
	}
	cmd.Flags().StringVar(&oldFile, "password-file", "", "read the CURRENT password from this file (for a change)")
	cmd.Flags().StringVar(&newFile, "new-password-file", "", "read the NEW password from this file (non-interactive)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return markGated(cmd)
}

type passwdFlags struct {
	oldFile string
	newFile string
	json    bool
}

func runVaultPasswd(cmd *cobra.Command, locName string, fl passwdFlags) error {
	ctx := cmd.Context()
	b, loc, err := locationBackend(locName)
	if err != nil {
		return err
	}

	// Is a password already set? (Stat the hash file.) A change is gated on the
	// old password; a first set is not.
	meta, serr := b.Stat(ctx, auth.HashFileRel)
	if serr != nil {
		return tserr.NodeOfflineErr(loc.Node, serr)
	}
	isChange := meta.Exists
	if isChange {
		if err := gateLocation(ctx, loc, b, locName, fl.oldFile); err != nil {
			return err
		}
	}

	// Read the NEW password (twice-confirmed on a TTY; from --new-password-file for
	// scripts/tests). Never a bare argv flag (visible in `ps`).
	newPw, err := auth.ReadPassword(auth.ReadOpts{
		PasswordFile: fl.newFile,
		Prompt:       "New password for " + locName + ": ",
		Confirm:      fl.newFile == "", // TTY: confirm; file: single source
	})
	if err != nil {
		return tserr.ConfigErr("vault passwd: read new password", err)
	}
	defer auth.Zero(newPw)
	if len(newPw) == 0 {
		return tserr.ConfigErr("vault passwd: empty password rejected", nil)
	}

	hf, err := auth.NewHashFile(newPw)
	if err != nil {
		return tserr.ConfigErr("vault passwd: derive hash", err)
	}
	phc := auth.FormatPHC(hf) + "\n"

	// WAL-logged mutation: intent → write hash file → done. BlobRefs locks the auth
	// file so a concurrent passwd serializes (WAL-as-lock).
	action := "set"
	if isChange {
		action = "change"
	}
	now := time.Now().UTC()
	opID := wal.NewOpID() // each passwd is distinct; no idempotent-resume semantics
	log := &wal.Log{B: b}
	if err := appendOpIntent(ctx, log, wal.Entry{
		OpID: opID, OpType: wal.OpPasswd, BlobRefs: []string{auth.HashFileRel},
		Actor: initActor(cmd), CreatedAt: now,
		Args: map[string]string{"action": action, "node": loc.Node},
	}, loc.Node, "passwd"); err != nil {
		return err
	}

	if err := b.PutOverwrite(ctx, auth.HashFileRel, strings.NewReader(phc)); err != nil {
		return tserr.NodeNotWritableErr(loc.Node, err)
	}
	// The hash file is a secret: enforce 0600 on the node (PutOverwrite writes with
	// the backend's default mode). Best-effort — the write already landed.
	secureHashFile(ctx, b, loc)

	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}

	if fl.json {
		return emitJSON(cmd, map[string]any{"location": locName, "node": loc.Node, "action": action})
	}
	w := cmd.OutOrStdout()
	if isChange {
		fmt.Fprintf(w, "password changed for %s\n", locName)
	} else {
		fmt.Fprintf(w, "password set for %s\n", locName)
	}
	fmt.Fprintln(w, "note  there is no recovery — reset requires SSH / physical access to the node (delete meta/auth/passwd)")
	return nil
}

// secureHashFile best-effort sets mode 0600 on the node's hash file after a
// PutOverwrite (which does not control file mode). SSH runs a remote chmod; a
// local mount (taildrive) chmods directly. Any failure is tolerated — the secret
// is already written; perms hardening is a defense-in-depth layer Block 5 audits.
func secureHashFile(ctx context.Context, b backend.Backend, loc locations.Location) {
	switch s := b.(type) {
	case *backend.SSH:
		_, _ = s.Exec(ctx, nil, "chmod 600 "+backend.ShellQuote(path.Join(loc.BasePath, auth.HashFileRel)))
	default:
		_ = os.Chmod(filepath.Join(loc.BasePath, filepath.FromSlash(auth.HashFileRel)), 0o600)
	}
}
