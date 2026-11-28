package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// initHomeFixture is a fresh repository with an acme remote, a temporary HOME
// and AGENTFLOW_HOME, and init's input replaced: terminal says whether a
// person is typing, input is what they type. It returns the repository, the
// home and the prompts init wrote.
func initHomeFixture(t *testing.T, terminal bool, input string) (repo, home string, prompts *bytes.Buffer) {
	t.Helper()
	repo, home = t.TempDir(), t.TempDir()
	t.Chdir(repo)
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_NO_UPDATE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/acme/demo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	prompts = &bytes.Buffer{}
	oldIn, oldTerm, oldOut := initStdin, initStdinIsTerminal, initPromptOut
	initStdin = strings.NewReader(input)
	initStdinIsTerminal = func() bool { return terminal }
	initPromptOut = prompts
	t.Cleanup(func() { initStdin, initStdinIsTerminal, initPromptOut = oldIn, oldTerm, oldOut })
	return repo, home, prompts
}

// providerDefaults accepts every provider question's default and the review.
var providerDefaults = strings.Repeat("\n", 20)

func requireRepoSetUp(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".agentflow", "config.yml")); err != nil {
		t.Fatalf("repository not set up: %v", err)
	}
}

func TestInitAsksHomeQuestionsInATerminal(t *testing.T) {
	// An empty mode answer is asked again: there is no default mode.
	repo, home, prompts := initHomeFixture(t, true, "acme, Example acme\n\nask\n"+providerDefaults)
	out, err := captureCLI(t, "init")
	if err != nil {
		t.Fatalf("%v\n%s", err, prompts)
	}
	requireRepoSetUp(t, repo)
	for _, want := range []string{"Personal setup", "Which GitHub owners", "auto (set the repository up)", "answer auto, ask or off"} {
		if !strings.Contains(prompts.String(), want) {
			t.Errorf("prompts lack %q:\n%s", want, prompts)
		}
	}
	if strings.Contains(out, "agentflow config set init.orgs") {
		t.Errorf("printed the questions after asking them:\n%s", out)
	}
	u, err := config.LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(u.Init.Orgs, []string{"acme", "Example"}) || u.Init.Auto != "ask" {
		t.Fatalf("saved init = %+v", u.Init)
	}
	if info, err := os.Stat(filepath.Join(home, "config.yml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("home config: %v %v", info, err)
	}
}

func TestInitSavesAnEmptyOwnerAnswer(t *testing.T) {
	repo, home, prompts := initHomeFixture(t, true, "\n"+providerDefaults)
	if _, err := captureCLI(t, "init"); err != nil {
		t.Fatalf("%v\n%s", err, prompts)
	}
	requireRepoSetUp(t, repo)
	if strings.Contains(prompts.String(), "auto (set the repository up)") {
		t.Errorf("asked for a mode without owners:\n%s", prompts)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.yml"))
	if err != nil || string(b) != "init:\n  orgs: []\n" {
		t.Fatalf("home config = %q %v", b, err)
	}
	// The home is initialised now: a second init asks nothing.
	prompts.Reset()
	initStdin = strings.NewReader(providerDefaults)
	if _, err := captureCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompts.String(), "Personal setup") {
		t.Errorf("asked again:\n%s", prompts)
	}
}

func TestInitWithoutATerminalWritesNothingToTheHome(t *testing.T) {
	for name, args := range map[string][]string{
		"no terminal":     nil,
		"non-interactive": {"--non-interactive"},
		// --guided reads piped answers for providers, but only a person at a
		// terminal answers the personal questions.
		"guided, piped": {"--guided"},
	} {
		t.Run(name, func(t *testing.T) {
			terminal := name == "non-interactive"
			repo, home, prompts := initHomeFixture(t, terminal, "acme\nauto\n"+providerDefaults)
			out, err := captureCLI(t, "init", args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, prompts)
			}
			requireRepoSetUp(t, repo)
			if _, err := os.Stat(filepath.Join(home, "config.yml")); !os.IsNotExist(err) {
				t.Fatalf("init wrote the home: %v", err)
			}
			if strings.Contains(prompts.String(), "Personal setup") {
				t.Errorf("asked without a terminal:\n%s", prompts)
			}
			for _, want := range []string{"Which GitHub owners", "auto (set the repository up)",
				"agentflow config set init.orgs=OWNER,OWNER init.auto=auto|ask|off", "agentflow config set init.orgs=\n"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestInitAsksNothingWhenTheHomeIsInitialised(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "terminal", false: "no terminal"}[terminal], func(t *testing.T) {
			repo, home, prompts := initHomeFixture(t, terminal, providerDefaults)
			const personal = "autonomy:\n  local: confirm\n"
			path := filepath.Join(home, "config.yml")
			if err := os.WriteFile(path, []byte(personal), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := captureCLI(t, "init")
			if err != nil {
				t.Fatalf("%v\n%s", err, prompts)
			}
			requireRepoSetUp(t, repo)
			if strings.Contains(prompts.String(), "Personal setup") || strings.Contains(out, "Personal setup") {
				t.Errorf("asked with an initialised home:\nprompts %s\nout %s", prompts, out)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != personal {
				t.Fatalf("home config changed: %q %v", b, err)
			}
		})
	}
}

func TestInitIgnoresAHomeInsideTheRepository(t *testing.T) {
	repo, _, prompts := initHomeFixture(t, true, "acme\noff\n"+providerDefaults)
	account := t.TempDir()
	t.Cleanup(config.OverrideAccountHome(account))
	oldWarn := config.HomeWarnings
	config.HomeWarnings = io.Discard
	t.Cleanup(func() { config.HomeWarnings = oldWarn })
	// A config.yml the repository supplies does not initialise the home.
	inRepo := filepath.Join(repo, "home")
	if err := os.MkdirAll(inRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	const supplied = "init:\n  orgs: [example]\n"
	if err := os.WriteFile(filepath.Join(inRepo, "config.yml"), []byte(supplied), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", inRepo)
	if _, err := captureCLI(t, "init"); err != nil {
		t.Fatalf("%v\n%s", err, prompts)
	}
	if !strings.Contains(prompts.String(), "Personal setup") {
		t.Fatalf("did not ask:\n%s", prompts)
	}
	if b, _ := os.ReadFile(filepath.Join(inRepo, "config.yml")); string(b) != supplied {
		t.Fatalf("wrote the in-repository home: %q", b)
	}
	b, err := os.ReadFile(filepath.Join(account, ".agentflow", "config.yml"))
	if err != nil || string(b) != "init:\n  auto: \"off\"\n  orgs:\n    - acme\n" {
		t.Fatalf("account home config = %q %v", b, err)
	}
}

func requireNoHomeConfig(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(home, "config.yml")); !os.IsNotExist(err) {
		t.Fatalf("home config was written: %v", err)
	}
}

func TestInitInspectionAsksNothing(t *testing.T) {
	for _, flagName := range []string{"--detect-providers", "--guidance-only"} {
		t.Run(flagName, func(t *testing.T) {
			_, home, prompts := initHomeFixture(t, true, "acme\nauto\n")
			out, err := captureCLI(t, "init", flagName)
			if err != nil {
				t.Fatal(err)
			}
			if prompts.Len() != 0 || strings.Contains(out, "Personal setup") {
				t.Errorf("asked: prompts %s\nout %s", prompts, out)
			}
			requireNoHomeConfig(t, home)
		})
	}
}

// The home, not the repository, decides whether init asks.
func TestInitAsksInARepositoryThatIsAlreadySetUp(t *testing.T) {
	_, home, prompts := initHomeFixture(t, false, "")
	if _, err := captureCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := captureCLI(t, "init")
	if err != nil || !strings.Contains(out, "agentflow config set init.orgs") {
		t.Fatalf("second run without a terminal: %v\n%s", err, out)
	}
	requireNoHomeConfig(t, home)
	initStdinIsTerminal = func() bool { return true }
	initStdin = strings.NewReader("\n" + providerDefaults)
	if _, err := captureCLI(t, "init"); err != nil || !strings.Contains(prompts.String(), "Personal setup") {
		t.Fatalf("terminal run: %v\n%s", err, prompts)
	}
}

func TestInitCancelledAtTheQuestionsSavesNothing(t *testing.T) {
	repo, home, _ := initHomeFixture(t, true, "")
	if _, err := captureCLI(t, "init"); err == nil || !strings.Contains(err.Error(), "personal setup cancelled") {
		t.Fatalf("err = %v", err)
	}
	requireNoHomeConfig(t, home)
	if _, err := os.Stat(filepath.Join(repo, ".agentflow")); !os.IsNotExist(err) {
		t.Fatalf("set the repository up after a cancel: %v", err)
	}
}

func TestInitAsksAgainForAnInvalidOwner(t *testing.T) {
	_, _, prompts := initHomeFixture(t, true, "acme/demo\nacme\nask\n"+providerDefaults)
	if _, err := captureCLI(t, "init"); err != nil {
		t.Fatalf("%v\n%s", err, prompts)
	}
	if !strings.Contains(prompts.String(), "is not a GitHub owner name") {
		t.Errorf("prompts:\n%s", prompts)
	}
}

// Answers are saved only when init succeeds, so the next run asks again.
func TestInitThatFailsKeepsNoAnswers(t *testing.T) {
	repo, home, prompts := initHomeFixture(t, true, "acme\nauto\n"+providerDefaults)
	// A file where init needs a folder makes the repository setup fail.
	if err := os.WriteFile(filepath.Join(repo, ".githooks"), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureCLI(t, "init"); err == nil {
		t.Fatalf("init succeeded:\n%s", prompts)
	}
	if !strings.Contains(prompts.String(), "Personal setup") {
		t.Fatalf("did not ask:\n%s", prompts)
	}
	requireNoHomeConfig(t, home)
}
