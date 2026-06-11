// Package filter implements tailvault's git clean/smudge driver: the two
// transforms git pipes a managed file's bytes through on staging and checkout.
//
//   - Clean  (git add/commit): if the path is vault-managed, hash the content
//     and emit the pointer; otherwise pass the bytes through untouched. push
//     re-scans the working tree to find and upload the blob, so clean's only job
//     is emitting the pointer — it stages nothing itself.
//   - Smudge (checkout): if stdin is a pointer, fetch the real bytes via the
//     backend and emit them (verifying integrity first); otherwise pass through.
//
// Both transforms are byte-exact and panic-free on arbitrary binary input, and
// are expressed as io.Reader/io.Writer functions so they unit-test without
// spawning git. A transform that errors must propagate non-zero so git aborts
// the operation rather than committing a pointer it can't resolve or checking
// out wrong bytes.
package filter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
	"github.com/Ibtesam-Mahmood/tailvault/internal/rules"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Env bundles the resolved config and backend the transforms need. Clean uses
// Cfg (+ Location for the pointer); Smudge uses Backend to fetch blobs.
type Env struct {
	Cfg      *config.Config
	Backend  backend.Backend
	Location string // pointer location field; falls back to Cfg.Storage.Location
}

func (e *Env) location() string {
	if e.Location != "" {
		return e.Location
	}
	if e.Cfg != nil {
		return e.Cfg.Storage.Location
	}
	return ""
}

// Clean reads the real file bytes from in. If path is vault-managed per the
// rules, it hashes the content and writes the canonical pointer to out;
// otherwise it copies the bytes verbatim. It must read the whole stream before
// emitting, since the sha needs all the bytes.
func Clean(_ context.Context, env *Env, path string, in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("filter-clean: read input: %w", err)
	}
	d, err := rules.Evaluate(env.Cfg, path, int64(len(data)))
	if err != nil {
		return fmt.Errorf("filter-clean: evaluate rules for %q: %w", path, err)
	}
	if !d.Managed {
		_, err := out.Write(data) // pass through untouched
		return err
	}
	sum := sha256.Sum256(data)
	p := pointer.Pointer{
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(data)),
		Location: env.location(),
	}
	_, err = out.Write(pointer.Encode(p))
	return err
}

// Smudge reads stdin from in. If the bytes are a pointer, it decodes it, fetches
// the real content via the backend, verifies the fetched bytes against the
// pointer (sha + size) and writes them to out. Otherwise it copies the bytes
// verbatim (a not-yet-cleaned or never-managed file).
//
// To avoid ever emitting wrong bytes, the fetched content is buffered to a temp
// file and integrity-checked before a single byte reaches out: a missing blob
// surfaces TV-OBJ-01 (exit 5) and a hash mismatch surfaces an integrity error,
// both leaving out empty so git aborts the checkout.
func Smudge(ctx context.Context, env *Env, in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("filter-smudge: read input: %w", err)
	}
	if !pointer.IsPointer(data) {
		_, err := out.Write(data) // pass through untouched
		return err
	}
	p, err := pointer.Decode(data)
	if err != nil {
		// A malformed pointer is a config/precondition failure; the command
		// boundary wraps this as tserr.ConfigErr (exit 2).
		return fmt.Errorf("filter-smudge: decode pointer: %w", err)
	}

	tmp, err := os.CreateTemp("", "tv-smudge-*")
	if err != nil {
		return fmt.Errorf("filter-smudge: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	h := sha256.New()
	key := "objects/" + p.SHA256
	if err := env.Backend.Get(ctx, key, io.MultiWriter(tmp, h)); err != nil {
		// Missing blob already arrives as a TV-OBJ-01 tserr.Error — propagate.
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return integrityErr(p.SHA256, fmt.Sprintf("content hashes to %s", got))
	}
	fi, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("filter-smudge: stat temp: %w", err)
	}
	if fi.Size() != p.Size {
		return integrityErr(p.SHA256, fmt.Sprintf("size %d != pointer size %d", fi.Size(), p.Size))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("filter-smudge: rewind temp: %w", err)
	}
	if _, err := io.Copy(out, tmp); err != nil {
		return fmt.Errorf("filter-smudge: write output: %w", err)
	}
	return nil
}

// integrityErr builds a TV-OBJ-01 (exit bucket 5) error for a blob whose fetched
// bytes do not match the pointer. It reuses the integrity bucket rather than the
// "missing" wording since the blob was fetched but is wrong.
func integrityErr(sha, detail string) error {
	return &tserr.Error{
		Code:  tserr.ObjMissing,
		Cause: fmt.Sprintf("blob %s failed integrity check: %s", sha, detail),
		Fix:   "re-push from a clone that has the correct content, or run `tailvault verify`",
	}
}
