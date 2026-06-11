package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeFetcher struct {
	rel   Release
	err   error
	calls int
}

func (f *fakeFetcher) Latest(context.Context) (Release, error) {
	f.calls++
	return f.rel, f.err
}

func TestCheck(t *testing.T) {
	f := &fakeFetcher{rel: Release{Tag: "v0.0.106"}}
	res, err := Check(context.Background(), f, "0.0.105")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.Latest != "0.0.106" || res.Current != "0.0.105" {
		t.Errorf("unexpected result: %+v", res)
	}

	res, _ = Check(context.Background(), &fakeFetcher{rel: Release{Tag: "v0.0.105"}}, "0.0.105")
	if res.Available {
		t.Error("equal versions should not report Available")
	}
}

func TestNoticeTextDisabled(t *testing.T) {
	t.Setenv("TAILVAULT_NO_UPDATE_CHECK", "1")
	f := &fakeFetcher{rel: Release{Tag: "v0.0.106"}}
	if got := NoticeText(context.Background(), f, "0.0.105", time.Now); got != "" {
		t.Errorf("disabled check should be silent, got %q", got)
	}
	if f.calls != 0 {
		t.Error("disabled check must not touch the network")
	}
}

func TestNoticeTextCacheLifecycle(t *testing.T) {
	t.Setenv("TAILVAULT_HOME", t.TempDir())
	t.Setenv("TAILVAULT_NO_UPDATE_CHECK", "")
	base := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

	// 1) Cold cache: fetches, writes cache, returns a notice.
	f := &fakeFetcher{rel: Release{Tag: "v0.0.106"}}
	got := NoticeText(context.Background(), f, "0.0.105", func() time.Time { return base })
	if !strings.Contains(got, "0.0.106") {
		t.Fatalf("cold check should surface the new version, got %q", got)
	}
	if f.calls != 1 {
		t.Fatalf("cold check should fetch once, got %d", f.calls)
	}

	// 2) Fresh cache (1h later): uses cache, no network. A failing fetcher proves
	// the network is not consulted.
	boom := &fakeFetcher{err: fmt.Errorf("network must not be called")}
	got = NoticeText(context.Background(), boom, "0.0.105", func() time.Time { return base.Add(time.Hour) })
	if !strings.Contains(got, "0.0.106") {
		t.Errorf("fresh cache should still surface the version, got %q", got)
	}
	if boom.calls != 0 {
		t.Error("fresh cache must not hit the network")
	}

	// 3) Stale cache (>TTL later): refetches.
	f2 := &fakeFetcher{rel: Release{Tag: "v0.0.107"}}
	got = NoticeText(context.Background(), f2, "0.0.105", func() time.Time { return base.Add(25 * time.Hour) })
	if !strings.Contains(got, "0.0.107") {
		t.Errorf("stale cache should refetch, got %q", got)
	}
	if f2.calls != 1 {
		t.Error("stale cache should refetch exactly once")
	}
}

func TestNoticeTextSilentOnFetchError(t *testing.T) {
	t.Setenv("TAILVAULT_HOME", t.TempDir())
	t.Setenv("TAILVAULT_NO_UPDATE_CHECK", "")
	f := &fakeFetcher{err: fmt.Errorf("offline")}
	if got := NoticeText(context.Background(), f, "0.0.105", time.Now); got != "" {
		t.Errorf("a failed check must be silent, got %q", got)
	}
}

func TestClientLatestParsesAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("token not forwarded: %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"tag_name":"v0.0.106","assets":[
			{"name":"tailvault_0.0.106_linux_amd64.tar.gz","url":"https://api/x"},
			{"name":"checksums.txt","url":"https://api/y"}]}`)
	}))
	defer srv.Close()

	cl := &Client{HTTP: srv.Client(), APIBase: srv.URL, Token: "tok"}
	rel, err := cl.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.0.106" || len(rel.Assets) != 2 {
		t.Fatalf("unexpected release: %+v", rel)
	}
	if _, ok := rel.asset("checksums.txt"); !ok {
		t.Error("checksums.txt asset not parsed")
	}
}

func TestClientLatestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	cl := &Client{HTTP: srv.Client(), APIBase: srv.URL}
	if _, err := cl.Latest(context.Background()); err == nil {
		t.Error("404 should surface a helpful error")
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("TAILVAULT_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "gh-tok")
	t.Setenv("GH_TOKEN", "other")
	if got := TokenFromEnv(); got != "gh-tok" {
		t.Errorf("TokenFromEnv = %q, want gh-tok", got)
	}
}
