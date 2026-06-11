package version

// Version is overwritten at build time via:
//
//	-ldflags "-X github.com/Ibtesam-Mahmood/tailvault/internal/version.Version=$(cat VERSION)"
//
// "dev" is the placeholder for `go run` / un-flagged `go build`.
var Version = "dev"
