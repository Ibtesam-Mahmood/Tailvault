package setup

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// StdinPrompter is the real Prompter: a dependency-light, stdlib-only
// implementation that reads line-oriented answers from In and writes prompts to
// Out. Keeping the interactive impl behind the Prompter interface means swapping
// in a richer TUI library later is a one-file change.
type StdinPrompter struct {
	In  io.Reader
	Out io.Writer
	r   *bufio.Reader
}

// NewStdinPrompter builds a StdinPrompter over the given streams.
func NewStdinPrompter(in io.Reader, out io.Writer) *StdinPrompter {
	return &StdinPrompter{In: in, Out: out, r: bufio.NewReader(in)}
}

func (p *StdinPrompter) reader() *bufio.Reader {
	if p.r == nil {
		p.r = bufio.NewReader(p.In)
	}
	return p.r
}

func (p *StdinPrompter) readLine() (string, error) {
	line, err := p.reader().ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF && line == "" {
		return "", err
	}
	return line, nil
}

// SelectPeer prints a numbered pick-list and reads the chosen index.
func (p *StdinPrompter) SelectPeer(peers []Peer) (Peer, error) {
	fmt.Fprintln(p.Out, "Select a node:")
	for i, pr := range peers {
		label := pr.Name
		if pr.IP != "" && pr.IP != pr.Name {
			label = fmt.Sprintf("%s (%s)", pr.Name, pr.IP)
		}
		fmt.Fprintf(p.Out, "  %d) %s\n", i+1, label)
	}
	fmt.Fprint(p.Out, "number [1]: ")
	line, err := p.readLine()
	if err != nil {
		return Peer{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return peers[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(peers) {
		return Peer{}, fmt.Errorf("setup: invalid selection %q", line)
	}
	return peers[n-1], nil
}

// AskString prompts for a free-text value, returning def on an empty answer.
func (p *StdinPrompter) AskString(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.Out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.Out, "%s: ", label)
	}
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// AskBackend prompts for the transfer backend, defaulting to ssh.
func (p *StdinPrompter) AskBackend() (string, error) {
	v, err := p.AskString("backend (ssh|taildrive)", DefaultBackend)
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(v)), nil
}
