package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
)

// The agentflow home counts as initialised once <home>/config.yml exists. Until
// then `agentflow init` in a terminal asks which GitHub owners the AI-client
// session hook may set up (init.orgs) and how (init.auto), and saves the
// answers there, an empty list included, so it asks only once. Without a
// terminal it writes nothing to the home and prints HomeQuestionsText instead.
// The session hook never asks and never writes the home.

// HomeQuestions says whether `agentflow init` should ask the personal setup
// questions, and where the answers go.
type HomeQuestions struct {
	// Needed is true when the home has no config.yml. init.orgs is read only
	// from that file, so it is then empty too.
	Needed bool
	// Path is <home>/config.yml, after HomeDir's check for a home inside a
	// repository.
	Path string
}

// CheckHomeQuestions reports whether the home is initialised. It reads the
// home through config.HomeDir, so a home inside a repository is replaced by
// the account's own folder, and it writes nothing.
func CheckHomeQuestions() (HomeQuestions, error) {
	dir, err := config.HomeDir()
	if err != nil {
		return HomeQuestions{}, err
	}
	q := HomeQuestions{Path: filepath.Join(dir, "config.yml")}
	if _, err := os.Lstat(q.Path); err == nil {
		return q, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return q, err
	}
	q.Needed = true
	return q, nil
}

// ownerRe matches a GitHub user or organisation name.
var ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// ParseOwners reads the answer to the owners question: names separated by
// commas or spaces. An empty answer means none.
func ParseOwners(answer string) ([]string, error) {
	owners := []string{}
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if !ownerRe.MatchString(f) {
			return nil, fmt.Errorf("%q is not a GitHub owner name", f)
		}
		if k := strings.ToLower(f); !seen[k] {
			seen[k] = true
			owners = append(owners, f)
		}
	}
	return owners, nil
}

// ParseAutoMode reads the answer to the mode question. There is no default:
// an empty answer is an error, so the caller asks again.
func ParseAutoMode(answer string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(answer)); m {
	case AutoInit, AutoAsk, AutoOff:
		return m, nil
	}
	return "", fmt.Errorf("answer %s, %s or %s", AutoInit, AutoAsk, AutoOff)
}

// homeAnswers is the whole config.yml written for the answers.
type homeAnswers struct {
	Init struct {
		Auto string   `yaml:"auto,omitempty"`
		Orgs []string `yaml:"orgs"`
	} `yaml:"init"`
}

// SaveHomeAnswers creates path (from CheckHomeQuestions) with init.orgs and,
// when owners were given, init.auto. It never replaces an existing file: if
// one appeared since the check, it returns an error and writes nothing.
func SaveHomeAnswers(path string, orgs []string, mode string) error {
	var doc homeAnswers
	doc.Init.Orgs = append([]string{}, orgs...)
	if len(orgs) > 0 {
		m, err := ParseAutoMode(mode)
		if err != nil {
			return err
		}
		doc.Init.Auto = m
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	b := buf.Bytes()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; change init.orgs with agentflow config set", path)
		}
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// Home question texts, shared by the terminal prompt and the printed hint.
const (
	HomeOwnersQuestion = "Which GitHub owners' repositories may agentflow set up automatically when an AI session starts there? " +
		"Names separated by commas; empty means none."
	HomeModeQuestion = "For those repositories: auto (set the repository up), ask (only suggest agentflow init) or off?"
)

// HomeQuestionsText is what init prints instead of asking when there is no
// terminal: the questions and the commands that save the person's answers.
// Only the person answers them; config set saves through the confirmation flow.
func HomeQuestionsText(path string) string {
	return "Personal setup: " + path + " does not exist yet, so agentflow has not asked these questions. " +
		"Nothing was written there.\n" +
		"  1. " + HomeOwnersQuestion + "\n" +
		"  2. " + HomeModeQuestion + "\n" +
		"Ask the person, then save the answers with:\n" +
		"  agentflow config set init.orgs=OWNER,OWNER init.auto=auto|ask|off\n" +
		"or, for no automatic setup:\n" +
		"  agentflow config set init.orgs=\n"
}
