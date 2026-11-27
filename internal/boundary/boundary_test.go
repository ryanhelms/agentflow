package boundary

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	home  = Repo{CommonDir: "/h/agent-commander/.git", Dir: "/h/agent-commander", Slug: "ryanhelms/agent-commander"}
	other = Repo{CommonDir: "/h/agentflow/.git", Dir: "/h/agentflow", Slug: "ryanhelms/agentflow"}
)

// fakeResolver knows the two checkouts above and a worktree of each.
func fakeResolver(dir string) (Repo, bool) {
	for prefix, r := range map[string]Repo{
		"/h/agent-commander": home, "/w/agent-commander": home,
		"/h/agentflow": other, "/w/agentflow": other,
	} {
		if dir == prefix || strings.HasPrefix(dir, prefix+"/") {
			return r, true
		}
	}
	return Repo{}, false
}

func checker() *Checker {
	return &Checker{
		Home: home, Cwd: "/h/agent-commander", Resolve: fakeResolver, UserHome: "/h",
		Getenv: func(k string) string {
			if k == "OTHER" {
				return "/h/agentflow"
			}
			return ""
		},
	}
}

func TestRefusesOtherRepository(t *testing.T) {
	for _, cmd := range []string{
		// the incident
		"gh pr merge 5 -R ryanhelms/agentflow",
		"gh pr merge 5 --squash --repo ryanhelms/agentflow",
		"gh pr merge 5 --repo=ryanhelms/agentflow",
		"gh -Rryanhelms/agentflow pr close 3",
		"gh pr merge https://github.com/ryanhelms/agentflow/pull/5 --squash",
		"GH_REPO=ryanhelms/agentflow gh pr merge 5",
		"export GH_REPO=ryanhelms/agentflow; gh issue create --title x --body y",
		"gh -R github.com/RyanHelms/AgentFlow release create v1",
		"gh -R agentflow pr create --title x",
		"gh api -X POST repos/ryanhelms/agentflow/issues -f title=x",
		"gh api repos/ryanhelms/agentflow/pulls/5/merge -X PUT",
		"gh api --method=DELETE /repos/ryanhelms/agentflow/git/refs/heads/x",
		"gh api repos/ryanhelms/agentflow/issues/1/comments -f body=hi",
		"gh repo create ryanhelms/new-thing --private",
		"gh repo delete ryanhelms/agentflow --yes",
		// process substitution runs the command inside
		"cat <(gh pr close 3 -R ryanhelms/agentflow)",
		"tee >(gh pr close 3 -R ryanhelms/agentflow) </dev/null",
		// gh takes the repository from the URL, not from -R or GH_REPO
		"gh pr merge https://github.com/ryanhelms/agentflow/pull/5 -R ryanhelms/agent-commander",
		"GH_REPO=ryanhelms/agent-commander gh pr close https://github.com/ryanhelms/agentflow/pull/5",
		// zsh runs =gh as the gh on PATH
		"=gh pr close 3 -R ryanhelms/agentflow",
		// git into another checkout or worktree
		"git -C /h/agentflow commit -m 'feat: x'",
		"git -C ../agentflow push origin feature",
		"git -C ~/agentflow merge main",
		"git --git-dir=/h/agentflow/.git branch -D old",
		"git --work-tree /w/agentflow/issue-1 reset --hard",
		"GIT_DIR=/h/agentflow/.git git tag v9",
		"cd /h/agentflow && git push",
		"cd ../agentflow; git commit -am 'fix: y'",
		"(cd /w/agentflow/issue-2 && git rebase main)",
		"pushd /h/agentflow && git checkout -b x",
		"cd $OTHER && git fetch",
		"cd /h/agentflow && gh pr merge 5",
		"cd /h/agentflow\ngit switch main",
		"bash -c 'cd /h/agentflow && git push'",
		"sh -c \"git -C /h/agentflow stash\"",
		"echo $(git -C /h/agentflow commit -m x)",
		"env GH_REPO=ryanhelms/agentflow gh pr close 5",
		"timeout 30 git -C /h/agentflow pull",
		"/usr/bin/git -C /h/agentflow worktree add /w/agentflow/x",
		"git -C /h/agentflow config user.name bot",
		"git -C /h/agentflow branch new-branch",
		"git -C /h/agentflow tag -d v1",
		"git -C /h/agentflow remote add up https://x",
		// pushing the home checkout to another GitHub repository
		"git push https://github.com/ryanhelms/agentflow.git HEAD:feature",
		"git push git@github.com:ryanhelms/agentflow.git feature",
	} {
		r := checker().Check(cmd)
		if r == nil {
			t.Errorf("allowed: %s", cmd)
			continue
		}
		msg := r.Message()
		for _, want := range []string{"agentflow delegate --repo", "agentflow_delegate", "ryanhelms/agent-commander"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message lacks %q: %s", cmd, want, msg)
			}
		}
	}
}

func TestAllowsHomeAndReads(t *testing.T) {
	for _, cmd := range []string{
		// the home repository and its worktrees
		"gh pr merge 5",
		"gh pr merge 5 -R ryanhelms/agent-commander",
		"gh pr merge https://github.com/ryanhelms/agent-commander/pull/5",
		"gh pr merge https://github.com/ryanhelms/agent-commander/pull/5 -R ryanhelms/agent-commander",
		"gh -R RyanHelms/Agent-Commander issue create --title x",
		"gh -R agent-commander pr close 2",
		"git commit -m 'feat: x'",
		"git push origin feature",
		"git push https://github.com/ryanhelms/agent-commander.git feature",
		"cd /w/agent-commander/issue-3 && git commit -m 'fix: z'",
		"git -C /w/agent-commander/issue-3 push",
		"gh api -X POST repos/ryanhelms/agent-commander/issues -f title=x",
		"gh api -X POST repos/{owner}/{repo}/issues -f title=x",
		// reads of another repository
		"gh pr view 5 -R ryanhelms/agentflow",
		"gh pr checks 5 --repo ryanhelms/agentflow --watch",
		"gh pr list -R ryanhelms/agentflow --state open",
		"gh pr diff https://github.com/ryanhelms/agentflow/pull/5",
		"gh run view 123 -R ryanhelms/agentflow --log",
		"gh release list -R ryanhelms/agentflow",
		"gh repo clone ryanhelms/agentflow /tmp/x",
		"gh repo view ryanhelms/agentflow",
		"gh api repos/ryanhelms/agentflow/pulls/5",
		"gh api repos/ryanhelms/agentflow/pulls -X GET -f state=open",
		"gh search prs --repo ryanhelms/agentflow",
		"gh api graphql -f query='mutation { x }'",
		"gh auth status",
		"git -C /h/agentflow status",
		"git -C /h/agentflow log --oneline -5",
		"git -C /h/agentflow diff main...HEAD",
		"git -C /h/agentflow branch -a",
		"git -C /h/agentflow branch --list 'issue-*'",
		"git -C /h/agentflow tag --list",
		"git -C /h/agentflow remote -v",
		"git -C /h/agentflow config --get remote.origin.url",
		"git -C /h/agentflow worktree list",
		"git -C /h/agentflow stash list",
		"cd /h/agentflow && git log && cd /h/agent-commander && git commit -m 'feat: x'",
		"(cd /h/agentflow && git status) && git push",
		// not a repository, or a new one
		"git -C /tmp/scratch commit -m x",
		"git clone https://github.com/ryanhelms/agentflow /tmp/af",
		"cd /tmp && git init demo",
		// text that only mentions commands
		"echo 'gh pr merge 5 -R ryanhelms/agentflow'",
		"git commit -m \"fix: refuse gh pr merge -R ryanhelms/agentflow\"",
		"git commit -F - <<'EOF'\nfix: x\n\ncd /h/agentflow && git push\nEOF",
		"git commit -m \"$(cat <<'EOF'\nfix: x\n\ngit -C /h/agentflow push\nEOF\n)\"",
		"ls -la 2>&1 | grep x > out.txt",
	} {
		if r := checker().Check(cmd); r != nil {
			t.Errorf("refused: %s: %s", cmd, r.Message())
		}
	}
}

func TestNoHomeRestrictsNothing(t *testing.T) {
	c := &Checker{Cwd: "/tmp", Resolve: fakeResolver}
	if r := c.Check("gh pr merge 5 -R ryanhelms/agentflow"); r != nil {
		t.Fatalf("refused without a home: %v", r)
	}
}

func TestHomeWithoutGitHubRemote(t *testing.T) {
	c := checker()
	c.Home = Repo{CommonDir: "/h/agent-commander/.git", Dir: "/h/agent-commander"}
	if r := c.Check("gh pr merge 5 -R ryanhelms/agentflow"); r == nil {
		t.Error("allowed another repository's merge")
	}
	if r := c.Check("gh pr merge 5 -R someone/agent-commander"); r != nil {
		t.Errorf("refused the home name: %v", r)
	}
}

func TestResolveDirWorktrees(t *testing.T) {
	dir := t.TempDir()
	main, wt, sibling := filepath.Join(dir, "main"), filepath.Join(dir, "wt"), filepath.Join(dir, "sibling")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", main)
	run("-C", main, "remote", "add", "origin", "git@github.com:RyanHelms/Agent-Commander.git")
	run("-C", main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	run("-C", main, "worktree", "add", "-q", "-b", "x", wt)
	run("init", "-q", sibling)
	ctx := context.Background()
	a, err := ResolveDir(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveDir(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !Same(a, b) || a.Slug != "ryanhelms/agent-commander" || a.Name() != "agent-commander" {
		t.Errorf("worktree is not the same repository: %+v %+v", a, b)
	}
	s, err := ResolveDir(ctx, sibling)
	if err != nil || Same(a, s) {
		t.Errorf("sibling: %+v %v", s, err)
	}
	if _, err := ResolveDir(ctx, filepath.Join(dir, "missing")); err == nil {
		t.Error("resolved a missing folder")
	}
	c := &Checker{Home: a, Cwd: wt, Resolve: GitResolver(ctx)}
	if r := c.Check("git -C " + sibling + " commit -m x"); r == nil {
		t.Error("allowed a commit in a sibling repository")
	}
	if r := c.Check("cd " + main + " && git commit -m x"); r != nil {
		t.Errorf("refused the primary checkout from a worktree: %v", r)
	}
}
