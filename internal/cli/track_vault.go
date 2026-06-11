package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// runTrackVault registers an already-present file (or glob) on a storage location
// with the vault — ingestion path 1 (D18.1). It works over the backend
// (local/taildrive mount or SSH), hashing on the node. An EXACT path overrides
// .tailvaultignore (D22); a GLOB respects it. Per the §16 frozen gated set,
// ingestion is NOT password-gated (only mutations of existing state are; DEV-46.7)
// — it rides tailnet ACL + SSH like reads.
func runTrackVault(cmd *cobra.Command, target string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	locName, pat, err := splitLocationPath(target)
	if err != nil {
		return err
	}
	be, loc, err := locationBackend(locName)
	if err != nil {
		return err
	}

	cat, err := readCatalog(ctx, be)
	if err != nil {
		return tserr.ConfigErr("track: read catalog", err)
	}
	if cat == nil {
		// track can be the first registration into a not-yet-bootstrapped vault.
		cat = &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: locName, Node: loc.Node}
	}
	ig, err := loadIgnoreBackend(ctx, be)
	if err != nil {
		return tserr.ConfigErr("track: read "+ingest.IgnoreFileName, err)
	}

	var rels []string
	var ignoredOverride []string
	if hasGlobMeta(pat) {
		keys, lerr := be.List(ctx, "")
		if lerr != nil {
			return lerr
		}
		for _, k := range keys {
			if isReservedKey(k) {
				continue
			}
			if _, ok := cat.Find(k); ok {
				continue // already tracked
			}
			if m, _ := doublestar.Match(pat, k); !m {
				continue
			}
			if ig.Match(k, nil) {
				continue // glob respects ignore
			}
			rels = append(rels, k)
		}
	} else {
		rels = []string{pat} // exact path: intent beats a glob ignore (D22)
		if ig.Match(pat, nil) {
			ignoredOverride = append(ignoredOverride, pat)
		}
	}

	results, err := ingest.Track(ctx, ingest.TrackOpts{
		Backend: be, Log: &wal.Log{B: be}, Cat: cat, Node: loc.Node, Actor: initActor(cmd),
	}, rels)
	if err != nil {
		if errors.Is(err, ingest.ErrPathNotPresent) {
			return tserr.ConfigErr("track: path not present on the vault disk: "+target, err)
		}
		return fmt.Errorf("track: %w", err)
	}

	for _, p := range ignoredOverride {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s is .tailvaultignore-listed; tracking anyway (explicit path overrides ignore)\n", p)
	}
	for _, r := range results {
		switch r.Status {
		case ingest.StatusTracked:
			fmt.Fprintf(out, "tracked %s (id %s)\n", r.Path, identity.Short(r.ID))
		case ingest.StatusAlready:
			fmt.Fprintf(out, "already tracked: %s (id %s)\n", r.Path, identity.Short(r.ID))
		case ingest.StatusDrifted:
			fmt.Fprintf(out, "already tracked: %s (id %s) — content changed since last scan; run `tailvault vault scan %s`\n",
				r.Path, identity.Short(r.ID), locName)
		}
	}
	if len(results) == 0 {
		fmt.Fprintln(out, "no matching untracked files")
	}
	return nil
}

// loadIgnoreBackend reads .tailvaultignore from the vault root over the backend
// (works local + remote); a missing file yields an empty Ignore.
func loadIgnoreBackend(ctx context.Context, be backend.Backend) (*ingest.Ignore, error) {
	var buf bytes.Buffer
	if err := be.Get(ctx, ingest.IgnoreFileName, &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return ingest.ParseIgnore(nil)
		}
		return nil, err
	}
	return ingest.ParseIgnore(buf.Bytes())
}

// isReservedKey reports whether a backend key is a vault-internal area never
// tracked (meta/git-flow areas + the ignore file itself).
func isReservedKey(key string) bool {
	if key == ingest.IgnoreFileName {
		return true
	}
	top := key
	if i := strings.IndexByte(key, '/'); i >= 0 {
		top = key[:i]
	}
	return top == "meta" || top == "objects" || top == "refs"
}

// hasGlobMeta reports whether s contains doublestar glob metacharacters.
func hasGlobMeta(s string) bool { return strings.ContainsAny(s, "*?[{") }
