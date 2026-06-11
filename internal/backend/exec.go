package backend

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// execRunner is the default Runner: it executes the command locally (e.g.
// `ssh user@node <remoteCmd>`), wiring stdin/stdout to the supplied streams and
// capturing stderr for error classification.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if in != nil {
		cmd.Stdin = in
	}
	if out != nil {
		cmd.Stdout = out
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.Bytes(), err
}
