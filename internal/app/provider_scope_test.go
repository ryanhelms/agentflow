package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

func writeUserDefinition(t *testing.T, providers string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("AGENTFLOW_HOME"), "providers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user.yml"), []byte("version: 1\nproviders:\n  "+providers), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLayeredJiraWriteScopeBeforeDispatch(t *testing.T) {
	a, host, nativeCtx := jiraNativeApp(t)
	for _, tc := range []struct {
		settings, project string
		allowed           bool
	}{
		{"{projects: [PLAT, OTHER]}", "OTHER", false},
		{"{projects: []}", "PLAT", false},
		{"{projects: [PLAT]}", "OPS", false},
		{"{projects: [PLAT]}", "PLAT", true},
		{"{site: https://other.example}", "PLAT", false},
	} {
		writeUserDefinition(t, "jira: {task_management: {settings: "+tc.settings+"}}")
		app, err := Open(t.Context(), a.Repo.Dir, a.Client)
		if err != nil {
			t.Fatal(err)
		}
		for _, ctx := range []context.Context{nativeCtx, t.Context()} {
			app.Gate.Autonomy.Remote = config.Confirm
			out, err := app.Comment(ctx, tc.project+"-1", "checked", "")
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				confirmation(t, out)
			} else if !errors.Is(err, native.ErrDenied) || out != nil {
				t.Errorf("settings %s, project %s: preview %v, error %v", tc.settings, tc.project, out, err)
			}
		}
	}
	if len(host.writes) != 0 {
		t.Fatalf("unexpected writes: %v", host.writes)
	}
	// Both the linked issue and every member of an EOD batch can be mutated
	// by the REST fallback. Reject the whole operation before a partial write.
	writeUserDefinition(t, "jira: {task_management: {settings: {projects: [PLAT, OTHER]}}}")
	app, err := Open(t.Context(), a.Repo.Dir, a.Client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateTicket(t.Context(), "PLAT", "Task", "Work", "", "OTHER-1", ""); !errors.Is(err, native.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := app.PostEOD(t.Context(), map[string]string{"PLAT-1": "here", "OTHER-1": "there"}, ""); !errors.Is(err, native.ErrDenied) {
		t.Fatal(err)
	}
}

func TestLayeredTaskTargetCannotWidenRepositoryScope(t *testing.T) {
	for _, tc := range []struct{ name, repo, user, scope string }{
		{"asana default", "asana, settings: {project: '10'}", "asana: {task_management: {settings: {project: '20'}}}", "20"},
		{"asana transition project", "asana, settings: {transitions: {'10': {done: completed}}}", "asana: {task_management: {settings: {transitions: {'20': {done: completed}}}}}", "20"},
		{"github repository", "github-issues, settings: {repository: org/trusted}", "github-issues: {task_management: {settings: {repository: org/other}}}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte("task_management: {provider: "+tc.repo+"}"), 0600); err != nil {
				t.Fatal(err)
			}
			writeUserDefinition(t, tc.user)
			a, err := Open(t.Context(), dir, "test")
			if err != nil {
				t.Fatal(err)
			}
			fallback := &fakeIssues{}
			a.tasks = fallback
			host := &taskWriteFixture{}
			for _, ctx := range []context.Context{taskWriteContext(t, host), t.Context()} {
				if out, err := a.CreateTicket(ctx, tc.scope, "", "Title", "", "", ""); !errors.Is(err, native.ErrDenied) || out != nil {
					t.Fatalf("unexpected preview %v, err %v", out, err)
				}
			}
			if host.preparations != 0 || len(fallback.writes) != 0 {
				t.Fatal("out-of-scope task reached a backend")
			}
		})
	}
}

func TestLayeredConfluenceSiteCannotRedirectPublication(t *testing.T) {
	a, fixture, in := newConfluenceFixture(t)
	writeUserDefinition(t, "confluence: {knowledge_base: {settings: {site: https://other.example}}}")
	app, err := Open(t.Context(), a.Repo.Dir, a.Client)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := app.DocsPublish(WithConfluenceCLI(t.Context()), in, ""); !errors.Is(err, native.ErrDenied) || out != nil || fixture.writes != 0 {
		t.Fatalf("publish: %v, %v", out, err)
	}
}

func TestLayeredConfluenceSpaceScopeBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, repo, user, space string
		allowed                 bool
	}{
		{"cannot widen", "[ENG]", "publish_spaces: [ENG, OTHER]", "OTHER", false},
		{"empty repository list", "[]", "publish_spaces: [ENG]", "ENG", false},
		{"cannot move space key to another site", "[ENG]", "site: https://other.example", "ENG", false},
		{"user can narrow", "[ENG, OPS]", "publish_spaces: [OPS]", "ENG", false},
		{"explicit empty user list", "[ENG]", "publish_spaces: []", "ENG", false},
		{"within both", "[ENG]", "publish_spaces: [ENG, OTHER]", "ENG", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
				t.Fatal(err)
			}
			// Only the list is a repository-supplied setting. Its keys still pin the pre-user site.
			body := "confluence: {site: https://repo.example}\n" +
				"knowledge_base: {provider: confluence, settings: {publish_spaces: " + tc.repo + "}}"
			if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			writeUserDefinition(t, "confluence: {knowledge_base: {settings: {"+tc.user+"}}}")
			a, err := Open(t.Context(), dir, "test")
			if err != nil {
				t.Fatal(err)
			}
			site, err := a.confluenceSite()
			if err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				if err := a.confluenceWriteScope(site, tc.space); err != nil {
					t.Fatal(err)
				}
				return
			}
			host := &confluenceMCPFixture{t: t}
			in := DocsPublishInput{Ref: provider.Ref{Instance: "confluence", Scope: tc.space, ID: "42"},
				Title: "Runbook", Body: "replacement", OperationID: "scope-check", ExpectedRevision: "7"}
			for _, ctx := range []context.Context{native.WithWriteHost(t.Context(), host), t.Context()} {
				if out, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, native.ErrDenied) || out != nil || host.prepares != 0 {
					t.Fatalf("unexpected publication route, result %v, err %v", out, err)
				}
			}
		})
	}
}

func TestLayeredSlackScopeBeforeRouteSelection(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	writeSlackNotificationConfig(t, dir, "notifications: {settings: {native: {workspace_id: T0123456789, channel_id: C0123456789, actor_id: U0123456789}}}")
	for _, override := range []string{"{native: {workspace_id: T1123456789}}", "{native: {channel_id: C1123456789}}", "{native: {actor_id: ''}}", "{native: null, credential_ref: OTHER_WEBHOOK}"} {
		writeUserDefinition(t, "slack: {notifications: {settings: "+override+"}}")
		a := openSlackNotificationFixture(t, dir, "test")
		host := newSlackNotifyHost(t, a.Home)
		_, pending, err := a.Notify(native.WithWriteHost(t.Context(), host), NotifyInput{Signal: "delivery", OperationID: "op", Body: "body", Automation: true}, "")
		if !errors.Is(err, native.ErrDenied) || pending != nil || host.prepares != 0 {
			t.Fatalf("%s: %v, %v", override, pending, err)
		}
	}
}

func TestRouterCannotAddTargetToRepositorySignal(t *testing.T) {
	dir := notificationFixtureRepo(t, "")
	writeSlackNotificationConfig(t, dir, `notifications:
  provider: router
  settings:
    destinations:
      team: {provider: slack, native: {workspace_id: T0123456789, channel_id: C0123456789}}
    routes: {delivery: [team]}
`)
	writeUserDefinition(t, `router:
    notifications:
      settings:
        destinations:
          personal: {provider: slack, native: {workspace_id: T1123456789, channel_id: C1123456789}}
        routes: {delivery: [team, personal]}`)
	a := openSlackNotificationFixture(t, dir, "test")
	host := newSlackNotifyHost(t, a.Home)
	_, pending, err := a.Notify(native.WithWriteHost(t.Context(), host), NotifyInput{Signal: "delivery", OperationID: "op", Body: "body"}, "")
	if !errors.Is(err, native.ErrDenied) || pending != nil || host.prepares != 0 {
		t.Fatalf("widened route: %v, %v", pending, err)
	}
}

func TestRepositoryRemoteBoundsResolvedIssueTarget(t *testing.T) {
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "code_hosting: {settings: {target_remote: upstream}}\ntask_management: {provider: github-issues}"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	writeUserDefinition(t, "auto: {code_hosting: {settings: {target_remote: origin}}}")
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/personal/fork.git")
	mustGit(t, a.Repo, "remote", "add", "upstream", "https://github.com/org/project.git")
	host := &taskWriteFixture{}
	ctx := taskWriteContext(t, host)
	if out, err := a.CreateTicket(ctx, "", "", "Follow up", "", "", ""); !errors.Is(err, native.ErrDenied) || out != nil || host.preparations != 0 {
		t.Fatalf("user-selected remote: %v, %v", out, err)
	}
	if err := a.taskWriteScope(t.Context(), "org/project"); err != nil {
		t.Fatal(err)
	}
}

// No site is built in, so a repository that lists spaces must also name the site.
// A personal setting cannot supply one for those space keys.
func TestAtlassianNeedsAConfiguredSite(t *testing.T) {
	open := func(t *testing.T, repo, user string) *App {
		t.Helper()
		dir := newRepo(t)
		if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(repo), 0600); err != nil {
			t.Fatal(err)
		}
		if user != "" {
			writeUserDefinition(t, user)
		}
		a, err := Open(t.Context(), dir, "test")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Run("jira", func(t *testing.T) {
		a := open(t, "task_management: {provider: jira}", "")
		if site, err := a.jiraSite(); err == nil || !strings.Contains(err.Error(), "no Jira site is configured") {
			t.Fatalf("site %q, err %v", site, err)
		}
	})
	t.Run("confluence", func(t *testing.T) {
		a := open(t, "knowledge_base: {provider: confluence}", "")
		if site, err := a.confluenceSite(); err == nil || !strings.Contains(err.Error(), "no Confluence site is configured") {
			t.Fatalf("site %q, err %v", site, err)
		}
	})
	t.Run("repository spaces without a site", func(t *testing.T) {
		a := open(t, "knowledge_base: {provider: confluence, settings: {publish_spaces: [ENG]}}",
			"confluence: {knowledge_base: {settings: {site: https://other.example}}}")
		site, err := a.confluenceSite()
		if err != nil {
			t.Fatal(err)
		}
		if err := a.confluenceWriteScope(site, "ENG"); !errors.Is(err, native.ErrDenied) {
			t.Fatalf("write to %s was not denied: %v", site, err)
		}
	})
}
