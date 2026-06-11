// Package integration holds tailvault's end-to-end scenario suite, exercising
// the real engine (push/pull/gc/verify/revert) over real backend transports
// against a temp "node" — never a developer's real tailnet.
//
// The scenario tests are guarded by the `integration` build tag so a plain
// `go test ./...` stays fast; run the full suite with:
//
//	go test -tags integration ./...
//
// This file carries no build tag so the package always has a buildable Go file
// (a tag-excluded test-only package would otherwise fail `go build ./...`).
package integration
