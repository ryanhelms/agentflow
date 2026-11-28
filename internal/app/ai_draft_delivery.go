package app

// The read methods of the commit, pr and ac drafts (ADR "Phase 8 features").
// They return raw material and refusals. They never stage, post or start
// anything; the features turn the material into prompt parts.
//
// Every diff here is gathered with --no-ext-diff and --no-textconv, and no
// repository or user git config may decide what the draft filter reads in a
// file header: the diffs this file runs fix the prefixes, and draftDiff
// rewrites the headers of all of them, gitx.Repo.PreviewCommit's included.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/commit"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/contextx"
	"github.com/ryanhelms/agentflow/internal/provider"
)

// DraftDiff is one change a draft may send.
type DraftDiff struct {
	Files string // "M path" lines
	Stat  string // "path | +1 -0" lines and a total
	// Diff is the unified diff with every file header written as
	// "diff --git a/OLD b/NEW", "--- a/OLD" and "+++ b/NEW".
	Diff string
	// Withheld are files whose content was already taken out of Diff
	// because the filter could not tell what they are from the header.
	Withheld []string
}

// DraftTask is the branch's task, as far as it could be read.
type DraftTask struct {
	Key, Title, Type, Description string
}

// CommitDraft is the material of a commit message draft.
type CommitDraft struct {
	DraftDiff
	Rules    string
	Commit   config.Commit
	KeyHint  string
	Task     DraftTask
	Source   string // the task provider
	Subjects string // the branch's last subjects
}

// PRDraft is the material of a PR title and body draft.
type PRDraft struct {
	DraftDiff
	Commits  string
	Task     DraftTask
	Source   string
	Criteria []contextx.Item
	Rules    string
	Commit   config.Commit
	KeyHint  string
	Template string // the default PR body
}

// ACDraft is the material of an acceptance-criteria check.
type ACDraft struct {
	Key      string
	Source   string
	Criteria []contextx.Item
	Branch   DraftDiff // the branch against its base
	Staged   DraftDiff
	Commits  string
}

func deliveryRefusal(code, message, fix string) error {
	return &aisession.Refusal{Code: code, Message: message, Fix: fix}
}

// CommitDraftContext reads what `commit` would record: the staged changes,
// or just paths, from a private index. The checkout is not changed.
func (a *App) CommitDraftContext(ctx context.Context, paths []string) (*CommitDraft, error) {
	nothing := deliveryRefusal("nothing_to_draft", "There is no change to write a commit message for.", "Stage a change, or name changed files with --path.")
	if len(paths) == 0 {
		staged, err := a.Repo.StagedFiles(ctx)
		if err != nil {
			return nil, err
		}
		if len(staged) == 0 {
			return nil, nothing
		}
	} else {
		status, err := a.Repo.Git(ctx, append([]string{"--no-optional-locks", "status", "--porcelain", "--"}, paths...)...)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(status) == "" {
			return nil, nothing
		}
	}
	tree, err := a.Repo.PreviewCommit(ctx, paths)
	if err != nil {
		return nil, err
	}
	d := &CommitDraft{DraftDiff: draftDiff(tree.Diff, tree.Files), Rules: a.rulesText(), Commit: a.Cfg.Commit, Source: a.draftTaskSource()}
	d.Task = a.draftTask(ctx)
	if a.UsesTaskRefs() {
		if ref, err := a.branchTask(ctx); err == nil && ref.ID != "" {
			d.Rules += "\nInclude this trailer after a blank line:\n" + provider.TaskTrailer + ref.String()
		}
	} else {
		d.KeyHint = a.CurrentKey(ctx) // as Validate uses it
	}
	// An unborn branch has no subjects.
	if out, err := a.Repo.Git(ctx, "log", "--no-merges", "--no-show-signature", "-n", "5", "--format=%s", "HEAD", "--"); err == nil {
		d.Subjects = withoutAddresses(strings.TrimSpace(out))
	}
	return d, nil
}

// PRDraftContext reads what the branch would publish against its base.
func (a *App) PRDraftContext(ctx context.Context) (*PRDraft, error) {
	base, from, err := a.draftBase(ctx)
	if err != nil {
		return nil, err
	}
	changes, err := a.draftRange(ctx, from, "HEAD")
	if err != nil {
		return nil, err
	}
	if changes.Diff == "" {
		return nil, deliveryRefusal("nothing_to_draft", "The branch has no committed change against "+base+".", "Commit your work, then draft again.")
	}
	commits, err := a.draftCommits(ctx, from)
	if err != nil {
		return nil, err
	}
	key := a.CurrentKey(ctx)
	d := &PRDraft{DraftDiff: changes, Commits: commits, Task: a.draftTask(ctx), Source: a.draftTaskSource(),
		Commit: a.Cfg.Commit, KeyHint: key, Rules: a.draftPRRules(key), Template: a.draftPRTemplate(ctx, key)}
	if key != "" {
		// The criteria are context here: a task that cannot be read has none.
		if _, items, err := a.draftCriteria(ctx, key); err == nil {
			d.Criteria = items
		}
	}
	return d, nil
}

// ACDraftContext reads the task's criteria, the branch's changes against its
// base and the staged changes. It does not write the local checklist.
func (a *App) ACDraftContext(ctx context.Context, key string) (*ACDraft, error) {
	if key == "" {
		key = a.CurrentKey(ctx)
	}
	if key == "" || a.Cfg.Providers.TaskManagement.Provider == "none" {
		return nil, deliveryRefusal("no_task", "There is no task for this branch.", "Pass the task key, or run agentflow start KEY.")
	}
	key, items, err := a.draftCriteria(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, deliveryRefusal("no_criteria", "The task has no acceptance criteria.", `Add an "Acceptance criteria" list to the task description.`)
	}
	_, from, err := a.draftBase(ctx)
	if err != nil {
		return nil, err
	}
	branch, err := a.draftRange(ctx, from, "HEAD")
	if err != nil {
		return nil, err
	}
	staged, err := a.draftRange(ctx, "--cached", "HEAD")
	if err != nil {
		return nil, err
	}
	if branch.Diff == "" && staged.Diff == "" {
		return nil, deliveryRefusal("nothing_to_draft", "The branch has no change to check the criteria against.", "Commit or stage your work, then draft again.")
	}
	commits, err := a.draftCommits(ctx, from)
	if err != nil {
		return nil, err
	}
	return &ACDraft{Key: key, Source: a.draftTaskSource(), Criteria: items, Branch: branch, Staged: staged, Commits: commits}, nil
}

// draftBase is the base pr.prepare would use and the merge base of HEAD with
// it: the remote-tracking ref when present, else the local branch. No fetch.
func (a *App) draftBase(ctx context.Context) (base, from string, err error) {
	target := "origin"
	if d, err := a.githubRemotes(ctx); err == nil {
		target = d.Target.Name
	}
	base = a.publicationBase(ctx, target)
	for _, ref := range []string{"refs/remotes/" + target + "/" + base, "refs/heads/" + base} {
		if _, err := a.Repo.Git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			continue
		}
		out, err := a.Repo.Git(ctx, "merge-base", ref, "HEAD")
		if err == nil {
			return base, strings.TrimSpace(out), nil
		}
	}
	return "", "", deliveryRefusal("no_base", "The branch's base, "+base+", cannot be resolved.", "Fetch the base branch, or set the remote's HEAD (git remote set-head).")
}

// draftRange is the diff from one side to the other; from may be "--cached".
// The flags keep repository and user config from running a program or
// changing the headers.
func (a *App) draftRange(ctx context.Context, from, to string) (DraftDiff, error) {
	diff := func(more ...string) (string, error) {
		args := []string{"-c", "core.quotepath=false", "diff", "--no-color", "--no-ext-diff", "--no-textconv",
			"--no-renames", "--submodule=short", "--src-prefix=a/", "--dst-prefix=b/"}
		args = append(append(args, more...), from, to, "--")
		return a.Repo.Git(ctx, args...)
	}
	names, err := diff("--name-only", "-z")
	if err != nil {
		return DraftDiff{}, err
	}
	text, err := diff()
	if err != nil {
		return DraftDiff{}, err
	}
	return draftDiff(text, strings.FieldsFunc(names, func(r rune) bool { return r == 0 })), nil
}

// headerPrefixes are the prefix pairs git writes in a file header, the
// ordinary one first.
var headerPrefixes = [][2]string{{"a/", "b/"}, {"", ""}, {"c/", "i/"}, {"c/", "w/"}, {"i/", "w/"}, {"i/", "c/"}, {"w/", "c/"}, {"w/", "i/"}, {"o/", "w/"}, {"1/", "2/"}}

// headerPaths reads the two names of a "diff --git" line. Both must be
// among the changed files: anything else is a form agentflow cannot read.
func headerPaths(line string, files map[string]bool) (from, to string, ok bool) {
	rest := strings.TrimPrefix(strings.TrimRight(line, "\r\n"), "diff --git ")
	name := func(token, prefix string) (string, bool) {
		if strings.HasPrefix(token, `"`) {
			unquoted, err := strconv.Unquote(token)
			if err != nil {
				return "", false
			}
			token = unquoted
		}
		n, found := strings.CutPrefix(token, prefix)
		return n, found && files[n]
	}
	for _, p := range headerPrefixes {
		for i := range len(rest) {
			if rest[i] != ' ' {
				continue
			}
			l, lok := name(rest[:i], p[0])
			r, rok := name(rest[i+1:], p[1])
			if lok && rok {
				return l, r, true
			}
		}
	}
	return "", "", false
}

// plainName reports whether a name can stand unquoted in a header.
func plainName(name string) bool {
	return !strings.ContainsFunc(name, func(r rune) bool { return r == '"' || r == '\\' || unicode.IsControl(r) }) &&
		!strings.Contains(name, " b/")
}

// shownName quotes a name that would not stand on one line of a list.
func shownName(name string) string {
	if strings.ContainsFunc(name, unicode.IsControl) {
		return strconv.Quote(name)
	}
	return name
}

// fileLines gives the "---" and "+++" lines of a section's header the names
// and prefixes of its rewritten first line.
func fileLines(section []string, from, to string) string {
	var b strings.Builder
	hunk := false
	for _, line := range section {
		hunk = hunk || strings.HasPrefix(line, "@@")
		switch {
		case hunk || strings.TrimRight(line, "\r\n") == "--- /dev/null" || strings.TrimRight(line, "\r\n") == "+++ /dev/null":
			b.WriteString(line)
		case strings.HasPrefix(line, "--- "):
			b.WriteString("--- a/" + from + "\n")
		case strings.HasPrefix(line, "+++ "):
			b.WriteString("+++ b/" + to + "\n")
		default:
			b.WriteString(line)
		}
	}
	return b.String()
}

// draftDiff rewrites every file header of a git diff to the one form the
// draft filter reads, and takes out the content of a file the header would
// not identify: one renamed or copied from a secret file, one whose name
// cannot stand in a header, and one in a section that cannot be read.
func draftDiff(diff string, changed []string) DraftDiff {
	files := make(map[string]bool, len(changed))
	for _, f := range changed {
		files[f] = true
	}
	var sections [][]string
	for _, line := range strings.SplitAfter(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			sections = append(sections, nil)
		}
		if len(sections) > 0 && line != "" {
			sections[len(sections)-1] = append(sections[len(sections)-1], line)
		}
	}
	var out DraftDiff
	var text, names, stat strings.Builder
	added, removed := 0, 0
	for _, s := range sections {
		from, to, ok := headerPaths(s[0], files)
		status, plus, minus, binary, hunk := "M", 0, 0, false, false
		for _, line := range s[1:] {
			switch {
			case strings.HasPrefix(line, "@@"):
				hunk = true
			case hunk && strings.HasPrefix(line, "+"):
				plus++
			case hunk && strings.HasPrefix(line, "-"):
				minus++
			case hunk:
			case strings.HasPrefix(line, "new file mode "):
				status = "A"
			case strings.HasPrefix(line, "deleted file mode "):
				status = "D"
			case strings.HasPrefix(line, "rename from "):
				status = "R"
			case strings.HasPrefix(line, "copy from "):
				status = "C"
			case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
				binary = true
			}
		}
		added, removed = added+plus, removed+minus
		if !ok {
			out.Withheld = append(out.Withheld, "(a file whose name could not be read)")
			text.WriteString("[a file's content left out: its name could not be read]\n")
			continue
		}
		label := shownName(to)
		if from != to {
			label = shownName(from) + " -> " + label
		}
		fmt.Fprintf(&names, "%s %s\n", status, label)
		if binary {
			fmt.Fprintf(&stat, "%s | binary\n", label)
		} else {
			fmt.Fprintf(&stat, "%s | +%d -%d\n", label, plus, minus)
		}
		secretFrom, secretTo := aisession.IsSecretFile(from), aisession.IsSecretFile(to)
		switch {
		case !plainName(from) || !plainName(to):
			hidden := shownName(to)
			if secretFrom {
				hidden = shownName(from)
			}
			out.Withheld = append(out.Withheld, hidden)
			text.WriteString("[a file's content left out: its name cannot be written in a header]\n")
		case secretFrom && !secretTo:
			out.Withheld = append(out.Withheld, from)
			fmt.Fprintf(&text, "diff --git a/%s b/%s\n[content left out: it comes from a secret file]\n", from, to)
		default:
			fmt.Fprintf(&text, "diff --git a/%s b/%s\n", from, to)
			text.WriteString(fileLines(s[1:], from, to))
		}
	}
	if len(sections) > 0 {
		fmt.Fprintf(&stat, "%d files changed, %d insertions(+), %d deletions(-)\n", len(sections), added, removed)
	}
	out.Diff, out.Files, out.Stat = text.String(), names.String(), stat.String()
	return out
}

var (
	byTrailerRe = regexp.MustCompile(`(?im)^[ \t]*[a-z][a-z-]*-by:.*(\r?\n|$)`)
	addressRe   = regexp.MustCompile(`<[^<>\s]+@[^<>\s]+>`)
)

// withoutAddresses drops the trailers that name people and every address in
// angle brackets: drafts send no author and no e-mail address.
func withoutAddresses(s string) string {
	return addressRe.ReplaceAllString(byTrailerRe.ReplaceAllString(s, ""), "<address>")
}

// draftCommits is the subjects and bodies of the branch's own commits,
// oldest first.
func (a *App) draftCommits(ctx context.Context, from string) (string, error) {
	out, err := a.Repo.Git(ctx, "log", "--no-merges", "--no-show-signature", "--reverse", "--format=%B%x00", from+"..HEAD", "--")
	if err != nil {
		return "", err
	}
	var msgs []string
	for _, m := range strings.Split(out, "\x00") {
		if m = strings.TrimSpace(withoutAddresses(m)); m != "" {
			msgs = append(msgs, m)
		}
	}
	return strings.Join(msgs, "\n\n---\n\n"), nil
}

// draftTaskSource names the task provider.
func (a *App) draftTaskSource() string {
	if p := a.Cfg.Providers.TaskManagement.Provider; p != "" {
		return p
	}
	return "jira"
}

// draftTask reads the branch's task. It is optional context for commit and
// PR drafts: whatever cannot be read is left empty.
func (a *App) draftTask(ctx context.Context) DraftTask {
	var t DraftTask
	if a.Cfg.Providers.TaskManagement.Provider == "none" {
		return t
	}
	var task provider.Task
	var err error
	if a.UsesTaskRefs() {
		ref, refErr := a.branchTask(ctx)
		if refErr != nil || ref.ID == "" {
			return t
		}
		t.Key = taskKey(ref)
		if task, err = a.getTask(ctx, ref); err == nil {
			t.Type = taskType(task)
		}
	} else {
		if t.Key = a.CurrentKey(ctx); t.Key == "" {
			return t
		}
		if task, err = a.jiraTask(ctx, t.Key); err == nil {
			t.Type = task.Type
		}
	}
	if err == nil {
		t.Title, t.Description = task.Title, task.Description
	}
	return t
}

// draftCriteria is the task's criteria with the local progress, as Checklist
// reads them, but without saving them. A task that cannot be read and has no
// saved checklist returns the provider's own error.
func (a *App) draftCriteria(ctx context.Context, key string) (string, []contextx.Item, error) {
	key, err := a.keyOrCurrent(ctx, key)
	if err != nil {
		return "", nil, err
	}
	store, storageKey := a.Checklists(ctx), key
	var task provider.Task
	var readErr error
	if a.UsesTaskRefs() {
		ref, err := a.taskRef(ctx, key)
		if err != nil {
			return "", nil, err
		}
		storageKey = ref.StorageKey()
		task, readErr = a.getTask(ctx, ref)
	} else {
		task, readErr = a.jiraTask(ctx, key)
	}
	var cl *contextx.Checklist
	if readErr == nil {
		cl, err = store.PreviewSync(storageKey, contextx.ParseCriteria(task.Description))
	} else {
		cl, err = store.Load(storageKey)
	}
	if err != nil {
		return "", nil, err
	}
	if cl == nil {
		return "", nil, readErr
	}
	return key, cl.Items, nil
}

// draftPRRules is the PR title rule as ValidatePRTitle applies it.
func (a *App) draftPRRules(key string) string {
	r := a.Cfg.Commit
	limit := r.SubjectMax
	if limit == 0 {
		limit = 72
	}
	subject := "type[(scope)][!]: summary"
	if r.KeyRequired() {
		subject = "type(KEY[,KEY])[!]: summary"
	}
	s := fmt.Sprintf("The pull request title becomes the squash commit subject.\nTitle: %s, at most %d characters. type is one of %s. Use an imperative summary with a lowercase start and no trailing period.",
		subject, limit-commit.SquashSuffixReserve, strings.Join(r.Types, ", "))
	if key != "" {
		s += "\nThe branch's task key is " + key + "."
	}
	return s
}

// prTemplatePaths are the templates prBody reads, in its order.
var prTemplatePaths = []string{".github/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE.md", "docs/pull_request_template.md"}

// draftPRTemplate is the default PR body, or "" when the template prBody
// would read leads out of the worktree, into .git or to a secret file: a
// link in the repository must not choose what leaves the machine.
func (a *App) draftPRTemplate(ctx context.Context, key string) string {
	root, err := filepath.EvalSymlinks(a.Repo.Dir)
	if err != nil {
		return ""
	}
	for _, p := range prTemplatePaths {
		target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ""
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || !filepath.IsLocal(rel) {
			return ""
		}
		rel = filepath.ToSlash(rel)
		first, _, _ := strings.Cut(rel, "/")
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() || strings.EqualFold(first, ".git") || aisession.IsSecretFile(rel) {
			return ""
		}
		break
	}
	return a.prBody(ctx, key)
}
