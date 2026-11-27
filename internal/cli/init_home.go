package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ryanhelms/agentflow/internal/setup"
)

// initStdin and initStdinIsTerminal are the input of `agentflow init` and
// whether a person is typing it; initPromptOut receives its questions. Tests
// replace them.
var (
	initStdin           io.Reader = os.Stdin
	initStdinIsTerminal           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	initPromptOut       io.Writer = os.Stderr
)

// askHomeQuestions asks the personal setup questions (see
// setup.CheckHomeQuestions) and saves the answers to path. End of input saves
// nothing. It reads one line per answer and no further, so the provider
// questions that follow read the rest of in.
func askHomeQuestions(path string, in io.Reader, out io.Writer) error {
	read := func(question string) (string, error) {
		fmt.Fprintf(out, "%s\n> ", question)
		line, err := readLine(in)
		if errors.Is(err, io.EOF) && line == "" {
			return "", errors.New("personal setup cancelled; nothing was saved")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	fmt.Fprintf(out, "Personal setup (asked once, saved to %s)\n", path)
	var owners []string
	for {
		answer, err := read(setup.HomeOwnersQuestion)
		if err != nil {
			return err
		}
		if owners, err = setup.ParseOwners(answer); err == nil {
			break
		}
		fmt.Fprintln(out, err)
	}
	mode := ""
	for len(owners) > 0 {
		answer, err := read(setup.HomeModeQuestion)
		if err != nil {
			return err
		}
		if mode, err = setup.ParseAutoMode(answer); err == nil {
			break
		}
		fmt.Fprintln(out, err)
	}
	if err := setup.SaveHomeAnswers(path, owners, mode); err != nil {
		return err
	}
	if len(owners) == 0 {
		fmt.Fprintf(out, "  saved    %s: no automatic setup\n", path)
	} else {
		fmt.Fprintf(out, "  saved    %s: init.orgs %s, init.auto %s\n", path, strings.Join(owners, ", "), mode)
	}
	return nil
}

// readLine reads up to and including the next newline, one byte at a time,
// so nothing after the line is consumed.
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}
