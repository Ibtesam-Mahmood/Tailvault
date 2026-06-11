// Package version exposes the tailvault build version. The value is set three
// ways, in order of precedence:
//
//  1. `-ldflags "-X .../internal/version.Version=<v>"` — what `make build` and
//     GoReleaser do; the authoritative path for released binaries.
//  2. Go module build info (`runtime/debug.ReadBuildInfo`) — the fallback for
//     `go install github.com/.../tailvault/cmd/tailvault@vX.Y.Z`, which ignores
//     ldflags. Without this, such installs would always report "dev".
//  3. "dev" — the placeholder for `go run` / un-flagged `go build` / installs
//     off an untagged commit.
package version

import "runtime/debug"

// Version is overwritten at build time via -ldflags (see package doc). When it
// is left at the "dev" placeholder, init() below tries the module build info so
// `go install ...@vX.Y.Z` still reports a real version.
var Version = "dev"

func init() {
	if Version != "dev" {
		return // ldflags already set the authoritative value.
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// Main.Version is the module version Go resolved for the install: a tag like
	// "v0.0.106" for `@vX.Y.Z`/`@latest`, or "(devel)" when built from a local
	// checkout. Only the former is a real release string.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		Version = v
	}
}
