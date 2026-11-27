package jsonrpc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// Child modes for Run. They run in the test binary (TestMain calls
// jsonrpctest.RunChild).
func init() {
	jsonrpctest.RegisterChild("run-envnames", func([]string) int {
		var names []string
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Println(strings.Join(names, "\n"))
		return 0
	})
	jsonrpctest.RegisterChild("run-env", func(args []string) int {
		fmt.Print(os.Getenv(args[0]))
		return 0
	})
	jsonrpctest.RegisterChild("run-exit", func(args []string) int {
		code, _ := strconv.Atoi(args[0])
		fmt.Print("out:" + args[1])
		fmt.Fprint(os.Stderr, "err:"+args[1]+"\n")
		return code
	})
	jsonrpctest.RegisterChild("run-pwd", func([]string) int {
		wd, _ := os.Getwd()
		fmt.Print(wd)
		return 0
	})
	jsonrpctest.RegisterChild("run-stdin", func([]string) int {
		var b bytes.Buffer
		_, _ = b.ReadFrom(os.Stdin)
		fmt.Print(b.Len())
		return 0
	})
	jsonrpctest.RegisterChild("run-flood-exit", func([]string) int {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		chunk[0] = 'A'
		for range 64 { // 4 MiB, far past any pipe buffer
			_, _ = os.Stdout.Write(chunk)
		}
		return 0
	})
	// run-spawn-exit starts a grandchild that sleeps and keeps stdout open,
	// prints its PID and exits; with "hang" it then sleeps instead.
	jsonrpctest.RegisterChild("run-spawn-exit", func(args []string) int {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, jsonrpctest.ChildArg, "sleep")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return 9
		}
		fmt.Println(cmd.Process.Pid)
		if len(args) > 0 && args[0] == "hang" {
			for {
				time.Sleep(time.Hour)
			}
		}
		return 0
	})
}

func runSpec(t *testing.T, mode string, args ...string) jsonrpc.LaunchSpec {
	t.Helper()
	return jsonrpctest.ChildSpec(t, mode, args...)
}

func run(t *testing.T, s jsonrpc.LaunchSpec, maxStdout int, timeout time.Duration) (jsonrpc.RunResult, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	res, err := jsonrpc.Run(ctx, s, maxStdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, time.Since(start)
}

func TestHarnessEnvPATHAndScrubbing(t *testing.T) {
	parent := []string{"LANG=C.UTF-8", "PATH=/opt/evil/bin:/usr/bin", "HOME=/Users/me", "USER=me"}
	for _, s := range secrets {
		parent = append(parent, s+"=secret-"+s)
	}
	data := t.TempDir()
	dirs := []string{filepath.Join(data, "bin-a"), filepath.Join(data, "bin-b")}
	env, err := jsonrpc.HarnessEnv(parent, data, dirs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(env) {
		t.Fatalf("not sorted: %v", env)
	}
	m := envMap(t, env)
	want := strings.Join(dirs, string(filepath.ListSeparator)) + string(filepath.ListSeparator)
	if !strings.HasPrefix(m["PATH"], want) {
		t.Fatalf("PATH = %q, want prefix %q", m["PATH"], want)
	}
	if strings.Contains(m["PATH"], "evil") {
		t.Fatalf("PATH inherited the parent's: %q", m["PATH"])
	}
	scrubbed, err := jsonrpc.ScrubbedEnv(parent, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	sm := envMap(t, scrubbed)
	if !strings.HasSuffix(m["PATH"], sm["PATH"]) {
		t.Fatalf("PATH = %q does not end in the fixed system PATH %q", m["PATH"], sm["PATH"])
	}
	delete(m, "PATH")
	delete(sm, "PATH")
	if fmt.Sprint(m) != fmt.Sprint(sm) {
		t.Fatalf("HarnessEnv differs from ScrubbedEnv beyond PATH:\n%v\n%v", m, sm)
	}
	if !strings.HasPrefix(m["HOME"], data) || m["HOME"] == "/Users/me" {
		t.Fatalf("HOME = %q, want under %s", m["HOME"], data)
	}
	for _, s := range append(secrets, "USER") {
		if _, ok := m[s]; ok {
			t.Errorf("%s reached the harness environment", s)
		}
	}

	if env, err := jsonrpc.HarnessEnv(parent, data, nil); err != nil || envMap(t, env)["PATH"] != sm2(t, parent, data) {
		t.Fatalf("no dirs: %v, %v", env, err)
	}
	for _, bad := range [][]string{{""}, {"bin"}, {"./bin"}, {data + string(filepath.ListSeparator) + "/x"}, {data + "\x00"}} {
		if _, err := jsonrpc.HarnessEnv(parent, data, bad); err == nil {
			t.Errorf("HarnessEnv accepted %q", bad)
		}
	}
	if _, err := jsonrpc.HarnessEnv(parent, "relative", dirs); err == nil {
		t.Error("HarnessEnv accepted a relative data directory")
	}
}

func sm2(t *testing.T, parent []string, data string) string {
	t.Helper()
	env, err := jsonrpc.ScrubbedEnv(parent, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return envMap(t, env)["PATH"]
}

// TestRunPassesNoCredential is the phase 6 security check: with credentials
// in the parent, a harness started through HarnessEnv and Run sees none.
func TestRunPassesNoCredential(t *testing.T) {
	adr := []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_HOME"}
	for _, name := range append(adr, secrets...) {
		t.Setenv(name, "secret-"+name)
	}
	s := runSpec(t, "run-envnames")
	data := t.TempDir()
	env, err := jsonrpc.HarnessEnv(os.Environ(), data, []string{s.Root})
	if err != nil {
		t.Fatal(err)
	}
	s.Env = env
	res, _ := run(t, s, 64<<10, 30*time.Second)
	if res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("result = %+v", res)
	}
	got := strings.Fields(string(res.Stdout))
	for _, name := range append(adr, secrets...) {
		if slices.Contains(got, name) {
			t.Errorf("%s reached the child", name)
		}
	}
	for _, name := range got {
		if !jsonrpc.EnvAllowed(name) {
			t.Errorf("%s reached the child but EnvAllowed refuses it", name)
		}
	}
	if !slices.Contains(got, "PATH") || !slices.Contains(got, "HOME") {
		t.Fatalf("child env names = %v", got)
	}
}

func TestRunExitCodeStdoutAndStderr(t *testing.T) {
	s := runSpec(t, "run-exit", "3", "hello")
	var stderr bytes.Buffer
	s.Stderr = &stderr
	res, _ := run(t, s, 1024, 30*time.Second)
	if res.ExitCode != 3 || res.TimedOut || string(res.Stdout) != "out:hello" {
		t.Fatalf("result = %+v (%q)", res, res.Stdout)
	}
	if stderr.String() != "err:hello\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}

	s = runSpec(t, "run-exit", "0", "abcdef")
	res, _ = run(t, s, 5, 30*time.Second)
	if string(res.Stdout) != "out:a" || res.ExitCode != 0 {
		t.Fatalf("capped result = %+v (%q)", res, res.Stdout)
	}
	s = runSpec(t, "run-exit", "0", "abcdef")
	if res, _ = run(t, s, 0, 30*time.Second); len(res.Stdout) != 0 {
		t.Fatalf("maxStdout 0 kept %q", res.Stdout)
	}
}

func TestRunStderrLimit(t *testing.T) {
	s := runSpec(t, "run-exit", "0", strings.Repeat("e", 300))
	var stderr bytes.Buffer
	s.Stderr = &stderr
	s.StderrLimit = 100
	run(t, s, 0, 30*time.Second)
	if stderr.String() != "[stderr truncated]\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFloodIsReadAndDropped(t *testing.T) {
	s := runSpec(t, "run-flood-exit")
	res, took := run(t, s, 4096, 30*time.Second)
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("result = %+v after %s", res, took)
	}
	if len(res.Stdout) != 4096 || res.Stdout[0] != 'A' {
		t.Fatalf("stdout = %d bytes starting %q, want the first 4096", len(res.Stdout), res.Stdout[:1])
	}
}

func TestRunTimeoutKills(t *testing.T) {
	s := runSpec(t, "sleep")
	res, took := run(t, s, 1024, 300*time.Millisecond)
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("result = %+v", res)
	}
	if took > 5*time.Second {
		t.Fatalf("Run took %s after its context ended", took)
	}
}

func TestRunStdinIsEmpty(t *testing.T) {
	res, _ := run(t, runSpec(t, "run-stdin"), 64, 30*time.Second)
	if string(res.Stdout) != "0" {
		t.Fatalf("stdin gave %q", res.Stdout)
	}
}

func TestRunWorkingDirectory(t *testing.T) {
	exe, root := copyChild(t)
	dir := filepath.Join(root, "work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := jsonrpctest.ChildSpec(t, "run-pwd")
	s.Path, s.Root, s.Dir = exe, root, dir
	res, _ := run(t, s, 4096, 30*time.Second)
	want, _ := filepath.EvalSymlinks(dir)
	if string(res.Stdout) != want {
		t.Fatalf("pwd = %q, want %q", res.Stdout, want)
	}
}

func TestRunRefusesLikeLaunch(t *testing.T) {
	good := runSpec(t, "run-exit", "0", "x")
	cases := map[string]func(s *jsonrpc.LaunchSpec){
		"relative path":  func(s *jsonrpc.LaunchSpec) { s.Path = "child" },
		"outside root":   func(s *jsonrpc.LaunchSpec) { s.Root = t.TempDir() },
		"disallowed env": func(s *jsonrpc.LaunchSpec) { s.Env = append(s.Env, "GITHUB_TOKEN=x") },
		"env without =":  func(s *jsonrpc.LaunchSpec) { s.Env = append(s.Env, "junk") },
		"dir outside":    func(s *jsonrpc.LaunchSpec) { s.Dir = t.TempDir() },
		"bad AllowEnv":   func(s *jsonrpc.LaunchSpec) { s.AllowEnv = []string{"A=B"} },
		"empty AllowEnv": func(s *jsonrpc.LaunchSpec) { s.AllowEnv = []string{""} },
		"unlisted variable": func(s *jsonrpc.LaunchSpec) {
			s.AllowEnv = []string{"USER"}
			s.Env = append(s.Env, "CODEX_HOME=/x")
		},
	}
	for name, mutate := range cases {
		s := good
		s.Env = slices.Clone(good.Env)
		mutate(&s)
		if _, err := jsonrpc.Run(context.Background(), s, 0); err == nil {
			t.Errorf("%s: Run accepted the spec", name)
		}
		if p, err := jsonrpc.Launch(context.Background(), s); err == nil {
			_ = p.Kill()
			<-p.Exited()
			t.Errorf("%s: Launch accepted the spec", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := jsonrpc.Run(ctx, good, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context: %v", err)
	}
}

func TestAllowEnvAddsOnlyListedNames(t *testing.T) {
	s := runSpec(t, "run-env", "USER")
	s.Env = append(slices.Clone(s.Env), "USER=someone")
	if _, err := jsonrpc.Run(context.Background(), s, 0); err == nil {
		t.Fatal("USER passed without AllowEnv")
	}
	s.AllowEnv = []string{"USER"}
	res, _ := run(t, s, 64, 30*time.Second)
	if string(res.Stdout) != "someone" {
		t.Fatalf("USER = %q", res.Stdout)
	}
}

// TestDataDirStatusProfile is the sign-in status profile's layout: HOME is
// the person's real home, DataDir is <home>/harnesses/<id> and Dir its
// status directory. With DataDir set, HOME's parent never counts as a data
// directory, whether AllowEnv is empty or not.
func TestDataDirStatusProfile(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "me") // the person's real home
	agentflowHome := filepath.Join(home, ".agentflow")
	data := filepath.Join(agentflowHome, "harnesses", "claude-acp")
	status := filepath.Join(data, "status")
	repo := filepath.Join(base, "work", "repo") // beside HOME, under its parent
	outside := t.TempDir()
	for _, d := range []string{status, repo, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(data, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symbolic links: %v", err)
	}

	for _, allow := range [][]string{nil, {"USER"}} {
		s := runSpec(t, "run-pwd")
		s.Env = append(withoutName(withoutName(s.Env, "HOME"), jsonrpc.EnvPluginData), "HOME="+home, "USER=me")
		if allow == nil {
			s.Env = withoutName(s.Env, "USER")
		}
		s.AllowEnv = allow
		s.DataDir = data

		s.Dir = status
		res, err := jsonrpc.Run(context.Background(), s, 4096)
		want, _ := filepath.EvalSymlinks(status)
		if err != nil || string(res.Stdout) != want {
			t.Fatalf("AllowEnv %v: status directory: %q, %v", allow, res.Stdout, err)
		}
		for name, dir := range map[string]string{"under HOME's parent": repo, "link out of DataDir": escape, "HOME itself": home} {
			s.Dir = dir
			if _, err := jsonrpc.Run(context.Background(), s, 64); err == nil {
				t.Errorf("AllowEnv %v: %s was accepted", allow, name)
			}
			if p, err := jsonrpc.Launch(context.Background(), s); err == nil {
				_ = p.Kill()
				<-p.Exited()
				t.Errorf("AllowEnv %v: Launch accepted %s", allow, name)
			}
		}
	}

	// Without DataDir the behaviour is as before: HOME's parent counts.
	s := runSpec(t, "run-pwd")
	s.Env = append(withoutName(withoutName(s.Env, "HOME"), jsonrpc.EnvPluginData), "HOME="+home)
	s.Dir = repo
	if _, err := jsonrpc.Run(context.Background(), s, 64); err != nil {
		t.Fatalf("without DataDir (unchanged behaviour): %v", err)
	}
}

func TestDataDirMustBeAnExistingAbsoluteDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative/data", filepath.Join(t.TempDir(), "missing"), file} {
		s := runSpec(t, "run-pwd")
		s.DataDir = bad
		if _, err := jsonrpc.Run(context.Background(), s, 64); err == nil {
			t.Errorf("DataDir %q accepted by Run", bad)
		}
		if p, err := jsonrpc.Launch(context.Background(), s); err == nil {
			_ = p.Kill()
			<-p.Exited()
			t.Errorf("DataDir %q accepted by Launch", bad)
		}
	}
}

func withoutName(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); n != name {
			out = append(out, kv)
		}
	}
	return out
}

// TestLaunchWithHarnessEnvPassesNoCredential is the same check for a probe,
// which uses Launch.
func TestLaunchWithHarnessEnvPassesNoCredential(t *testing.T) {
	adr := []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_HOME"}
	for _, name := range append(adr, secrets...) {
		t.Setenv(name, "secret-"+name)
	}
	s := jsonrpctest.ChildSpec(t, "serve")
	env, err := jsonrpc.HarnessEnv(os.Environ(), t.TempDir(), []string{s.Root})
	if err != nil {
		t.Fatal(err)
	}
	s.Env = env
	p := launch(t, s)
	var got []string
	if err := p.Conn().Call(context.Background(), "env", nil, &got); err != nil {
		t.Fatal(err)
	}
	m := envMap(t, got)
	for _, name := range append(adr, secrets...) {
		if _, ok := m[name]; ok {
			t.Errorf("%s reached the child", name)
		}
	}
	if !strings.HasPrefix(m["PATH"], s.Root+string(filepath.ListSeparator)) {
		t.Fatalf("PATH = %q, want %s first", m["PATH"], s.Root)
	}
}
