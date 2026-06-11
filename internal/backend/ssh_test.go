package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// scriptedRunner answers each ssh invocation from a handler that inspects the
// remote command (last arg) and writes canned stdout / returns canned
// stderr+err. It also drains stdin so Put's reader is consumed.
type scriptedRunner struct {
	handle func(remoteCmd string, in io.Reader, out io.Writer) (stderr []byte, err error)
	calls  []string
}

func (r *scriptedRunner) Run(_ context.Context, in io.Reader, out io.Writer, _ string, args ...string) ([]byte, error) {
	remoteCmd := args[len(args)-1]
	r.calls = append(r.calls, remoteCmd)
	if r.handle == nil {
		return nil, nil
	}
	return r.handle(remoteCmd, in, out)
}

func newSSH(r Runner) *SSH {
	return &SSH{User: "ibte", Node: "home-pi", BasePath: "/mnt/ssd/tailvault", R: r}
}

func TestSSH_PingFailure_TVNODE01(t *testing.T) {
	s := newSSH(&scriptedRunner{})
	s.Ping = func(context.Context, string) error { return errors.New("ping: 100% loss") }

	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"Stat", func() error { _, e := s.Stat(context.Background(), "objects/x"); return e }},
		{"Get", func() error { return s.Get(context.Background(), "objects/x", &bytes.Buffer{}) }},
		{"Put", func() error { return s.Put(context.Background(), "objects/x", strings.NewReader("d")) }},
	} {
		err := op.run()
		var te *tserr.Error
		if !errors.As(err, &te) || te.Code != tserr.NodeOffline {
			t.Errorf("%s on ping fail: want TV-NODE-01, got %v", op.name, err)
		}
	}
}

func TestSSH_PreflightRunsBeforeTransfer(t *testing.T) {
	// A ping failure must abort BEFORE any ssh command runs (no partial upload).
	r := &scriptedRunner{}
	s := newSSH(r)
	s.Ping = func(context.Context, string) error { return errors.New("down") }
	_ = s.Put(context.Background(), "objects/x", strings.NewReader("data"))
	if len(r.calls) != 0 {
		t.Errorf("ssh invoked %d times despite ping failure; want 0 (%v)", len(r.calls), r.calls)
	}
}

func TestSSH_Stat(t *testing.T) {
	ctx := context.Background()

	present := newSSH(&scriptedRunner{handle: func(_ string, _ io.Reader, out io.Writer) ([]byte, error) {
		io.WriteString(out, "42\n")
		return nil, nil
	}})
	m, err := present.Stat(ctx, "objects/x")
	if err != nil || !m.Exists || m.Size != 42 {
		t.Fatalf("Stat(present) = %+v, %v; want {true,42}", m, err)
	}

	absent := newSSH(&scriptedRunner{handle: func(_ string, _ io.Reader, out io.Writer) ([]byte, error) {
		io.WriteString(out, missingMarker+"\n")
		return nil, nil
	}})
	m, err = absent.Stat(ctx, "objects/x")
	if err != nil || m.Exists {
		t.Fatalf("Stat(absent) = %+v, %v; want {false}, nil", m, err)
	}
}

func TestSSH_Get_Missing_TVOBJ01(t *testing.T) {
	s := newSSH(&scriptedRunner{handle: func(_ string, _ io.Reader, _ io.Writer) ([]byte, error) {
		return []byte(missingMarker + "\n"), errors.New("exit status 7")
	}})
	err := s.Get(context.Background(), "objects/x", &bytes.Buffer{})
	if !errors.Is(err, ErrNotExist) {
		t.Errorf("Get(missing): errors.Is ErrNotExist = false; got %v", err)
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Errorf("Get(missing): want TV-OBJ-01, got %v", err)
	}
}

func TestSSH_Put_Dedup(t *testing.T) {
	// If Stat reports the key exists, Put must not issue a write command.
	r := &scriptedRunner{handle: func(remoteCmd string, _ io.Reader, out io.Writer) ([]byte, error) {
		if strings.Contains(remoteCmd, "wc -c") { // the Stat probe
			io.WriteString(out, "5\n")
		}
		return nil, nil
	}}
	s := newSSH(r)
	if err := s.Put(context.Background(), "objects/x", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "mv ") || strings.Contains(c, "cat >") {
			t.Errorf("dedup failed: write command issued: %q", c)
		}
	}
}

func TestSSH_Put_PermissionDenied_TVNODE02(t *testing.T) {
	r := &scriptedRunner{handle: func(remoteCmd string, in io.Reader, _ io.Writer) ([]byte, error) {
		if strings.Contains(remoteCmd, "wc -c") {
			return []byte(missingMarker), nil // Stat: absent, proceed to write
		}
		io.Copy(io.Discard, in) // drain stdin
		return []byte("mkdir: cannot create directory: Permission denied\n"), errors.New("exit status 1")
	}}
	s := newSSH(r)
	err := s.Put(context.Background(), "objects/x", strings.NewReader("data"))
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.NodeNotWritable {
		t.Errorf("Put on permission denied: want TV-NODE-02, got %v", err)
	}
}

func TestSSH_Get_RoundTrip(t *testing.T) {
	s := newSSH(&scriptedRunner{handle: func(_ string, _ io.Reader, out io.Writer) ([]byte, error) {
		io.WriteString(out, "blob-bytes")
		return nil, nil
	}})
	var buf bytes.Buffer
	if err := s.Get(context.Background(), "objects/x", &buf); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if buf.String() != "blob-bytes" {
		t.Errorf("Get = %q, want %q", buf.String(), "blob-bytes")
	}
}

func TestSSH_List(t *testing.T) {
	s := newSSH(&scriptedRunner{handle: func(_ string, _ io.Reader, out io.Writer) ([]byte, error) {
		io.WriteString(out, "/mnt/ssd/tailvault/objects/aaa\n/mnt/ssd/tailvault/objects/bbb\n/mnt/ssd/tailvault/refs/r1\n")
		return nil, nil
	}})
	keys, err := s.List(context.Background(), "objects/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"objects/aaa", "objects/bbb"}
	if len(keys) != len(want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/mnt/ssd/tailvault":  `'/mnt/ssd/tailvault'`,
		"o'brien/objects/sha": `'o'\''brien/objects/sha'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSH_NoUser_Target(t *testing.T) {
	s := &SSH{Node: "home-pi", BasePath: "/v"}
	if s.target() != "home-pi" {
		t.Errorf("target() without user = %q, want home-pi", s.target())
	}
}
