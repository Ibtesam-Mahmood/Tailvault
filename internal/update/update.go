package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repo coordinates. Centralised so the GitHub API URLs and the GoReleaser asset
// naming convention have a single source of truth.
const (
	Owner = "Ibtesam-Mahmood"
	Repo  = "tailvault"

	// apiBase is the GitHub REST endpoint; overridable in tests via Client.APIBase.
	apiBase = "https://api.github.com"

	// cacheTTL bounds how often the passive notice hits the network. ~once/day.
	cacheTTL = 24 * time.Hour
)

// Asset is one published file on a release (a platform archive or checksums.txt).
type Asset struct {
	Name string // e.g. "tailvault_0.0.106_darwin_arm64.tar.gz"
	// APIURL is the api.github.com asset URL. Downloading through it (with
	// Accept: application/octet-stream and a token) works for both public and
	// private repos, unlike browser_download_url which 404s on private repos
	// without a session.
	APIURL string
}

// Release is the slice of a GitHub release tailvault cares about.
type Release struct {
	Tag    string // e.g. "v0.0.106"
	Assets []Asset
}

// asset returns the named asset, or ok=false.
func (r Release) asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Fetcher resolves the latest release. The real implementation hits GitHub; tests
// inject a fake so no network is touched.
type Fetcher interface {
	Latest(ctx context.Context) (Release, error)
}

// Client is the real GitHub-backed Fetcher (and the downloader). Token, when
// non-empty, authenticates against a private repo. The zero value is usable
// except that HTTP defaults to http.DefaultClient and APIBase to the public API.
type Client struct {
	HTTP    *http.Client
	Token   string // GITHUB_TOKEN / GH_TOKEN; empty for a public repo
	APIBase string // overridable in tests; defaults to apiBase
}

// NewClient builds a Client from the environment: a short HTTP timeout (the
// passive check must never hang a command) and a token discovered from the
// usual env vars.
func NewClient() *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 15 * time.Second},
		Token: TokenFromEnv(),
	}
}

// TokenFromEnv returns the first set of the conventional GitHub token vars, or "".
func TokenFromEnv() string {
	for _, k := range []string{"TAILVAULT_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) base() string {
	if c.APIBase != "" {
		return c.APIBase
	}
	return apiBase
}

func (c *Client) authReq(ctx context.Context, method, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

// Latest implements Fetcher against GET /repos/{owner}/{repo}/releases/latest.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.base(), Owner, Repo)
	req, err := c.authReq(ctx, http.MethodGet, url, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("no published release found (or repo access denied — set GITHUB_TOKEN for a private repo)")
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decode release JSON: %w", err)
	}
	rel := Release{Tag: body.TagName}
	for _, a := range body.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, APIURL: a.URL})
	}
	return rel, nil
}

// download fetches an asset's bytes via its API URL (public + private safe).
func (c *Client) download(ctx context.Context, a Asset) ([]byte, error) {
	req, err := c.authReq(ctx, http.MethodGet, a.APIURL, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: GitHub returned %s", a.Name, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// CheckResult is the outcome of comparing the running build to the latest release.
type CheckResult struct {
	Current   string // the running build's version ("dev" if unknown)
	Latest    string // latest release tag, normalised (no leading v) — "" if unknown
	Available bool   // a strictly-newer release exists
}

// Check fetches the latest release and compares it to current. The caller passes
// the running version (version.Version) so this package stays decoupled from it.
func Check(ctx context.Context, f Fetcher, current string) (CheckResult, error) {
	rel, err := f.Latest(ctx)
	if err != nil {
		return CheckResult{Current: current}, err
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v")
	return CheckResult{
		Current:   current,
		Latest:    latest,
		Available: NewerAvailable(current, latest),
	}, nil
}

// --- passive notice cache ---------------------------------------------------

// cacheEntry is the on-disk record backing the passive notice. It is advisory:
// any read/parse error is treated as "no cache" and silently ignored.
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

// stateDir is ~/.tailvault — the same client-side state dir documented in the
// README (pull receipts, federation cache). Overridable via TAILVAULT_HOME for
// tests and unusual layouts.
func stateDir() (string, error) {
	if h := os.Getenv("TAILVAULT_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tailvault"), nil
}

func cachePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "update-check.json"), nil
}

func readCache() (cacheEntry, bool) {
	p, err := cachePath()
	if err != nil {
		return cacheEntry{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil {
		return cacheEntry{}, false
	}
	return e, true
}

func writeCache(e cacheEntry) {
	p, err := cachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(e); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// NoticeDisabled reports whether the passive update notice is switched off via
// the env var. (A repo-config knob can layer on top of this later.)
func NoticeDisabled() bool {
	v := strings.TrimSpace(os.Getenv("TAILVAULT_NO_UPDATE_CHECK"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// NoticeText returns a one-line "update available" string for long-lived
// commands to append, or "" when nothing should be shown. It is best-effort and
// must never block the real command:
//
//   - disabled via env → "".
//   - cache fresh (< TTL) → compare cached tag to current, no network.
//   - cache stale/missing → one bounded fetch (caller's ctx, already short via
//     NewClient's timeout); refresh the cache; on any error, silently return "".
//
// now is injected for deterministic tests; production passes time.Now.
func NoticeText(ctx context.Context, f Fetcher, current string, now func() time.Time) string {
	if NoticeDisabled() {
		return ""
	}
	if e, ok := readCache(); ok && now().Sub(e.CheckedAt) < cacheTTL {
		return noticeFor(current, e.LatestTag)
	}
	rel, err := f.Latest(ctx)
	if err != nil {
		return "" // never surface a failed check during a normal command
	}
	writeCache(cacheEntry{CheckedAt: now(), LatestTag: rel.Tag})
	return noticeFor(current, rel.Tag)
}

func noticeFor(current, latestTag string) string {
	latest := strings.TrimPrefix(strings.TrimSpace(latestTag), "v")
	if latest == "" || !NewerAvailable(current, latest) {
		return ""
	}
	return fmt.Sprintf("⬆ tailvault %s is available (you have %s). Run `tailvault update`.", latest, current)
}
