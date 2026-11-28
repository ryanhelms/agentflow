package ai_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// sources returns the non-test Go files of internal/ai and its packages,
// test helpers (aitest) left out.
func sources(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "aitest" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out[p] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no sources found")
	}
	return out
}

const module = "github.com/ryanhelms/agentflow/"

// TestAIImports: internal/ai and internal/ai/features import only the
// packages "Phase 8 package dependencies" allows, so they cannot reach the
// Invoker, the gate or a surface.
func TestAIImports(t *testing.T) {
	allowed := []string{"internal/app", "internal/aisession", "internal/commit", "internal/config", "internal/contextx",
		"internal/gh", "internal/gitx", "internal/harness", "internal/provider", "internal/tasktemplate",
		// acp is aisession's protocol types (stop reasons); it runs nothing.
		"internal/acp", "internal/ai"}
	never := []string{"internal/command", "internal/webui", "internal/mcpserver", "internal/cli", "internal/plugin", "internal/policy"}
	for name, f := range sources(t) {
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, module) {
				if strings.Contains(p, ".") {
					t.Errorf("%s imports %s: no dependency outside the standard library", name, p)
				}
				continue
			}
			rel := strings.TrimPrefix(p, module)
			for _, n := range never {
				if rel == n || strings.HasPrefix(rel, n+"/") {
					t.Errorf("%s imports %s, which it must never import", name, rel)
				}
			}
			ok := false
			for _, a := range allowed {
				if rel == a || strings.HasPrefix(rel, a+"/") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s imports %s, which is not on the allowed list", name, rel)
			}
		}
	}
}

// The read allowlist of "Phase 8 nothing is applied automatically".
var (
	appReads  = []string{"CommitDraftContext", "PRDraftContext", "ACDraftContext", "TicketDraftContext", "ReplyDraftContext", "ReviewThreads", "Checklist", "Context", "CurrentKey", "UsesTaskRefs"}
	repoReads = []string{"PreviewCommit", "Git", "Branch", "StagedFiles", "DiffStat"}
	gitReads  = []string{"diff", "log", "merge-base", "rev-parse", "symbolic-ref", "show", "status"}
	// Package functions and variables of internal/app a draft may use: the
	// secret scanners (Filter step 5, Finish step 5) and the session
	// Manager constructor. The ADR's list names DraftSecrets only; see the
	// lane report.
	appFuncs = []string{"DraftSecrets", "ContainsSecret", "NewAIManager", "SessionEnv"}
)

// TestDraftNeverWrites parses the sources and fails on any call of an App
// method, a Repo method or an app function outside the read allowlist, and
// on a git subcommand that is not a read.
func TestDraftNeverWrites(t *testing.T) {
	for name, f := range sources(t) {
		appName := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == module+"internal/app" {
				appName = "app"
				if imp.Name != nil {
					appName = imp.Name.Name
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// app.F: only the allowed functions and variables (types are
				// fine: they run nothing).
				if id, ok := x.X.(*ast.Ident); ok && appName != "" && id.Name == appName {
					if r := []rune(x.Sel.Name); len(r) > 0 && !slices.Contains(appFuncs, x.Sel.Name) && isFuncLike(x.Sel.Name) {
						t.Errorf("%s uses app.%s, which is not on the read allowlist", name, x.Sel.Name)
					}
				}
			case *ast.FuncDecl:
				checkFunc(t, name, appName, x)
				checkManager(t, name, x)
			}
			return true
		})
	}
}

// managerReads are the session Manager methods a draft may call: Prepare
// and Start run the draft itself, the rest only read. Answer, Cancel, Host
// and Prune act on sessions and are never called from internal/ai.
var managerReads = []string{"Prepare", "Start", "List", "Get", "Items", "Draft"}

// checkManager fails on a call of a Manager method outside managerReads, on
// a Manager made in fn (manager(o), aisession.NewManager(o),
// app.NewAIManager(o)) or a variable holding one.
func checkManager(t *testing.T, file string, fn *ast.FuncDecl) {
	if fn.Body == nil {
		return
	}
	isMaker := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			return f.Name == "manager"
		case *ast.SelectorExpr:
			return f.Sel.Name == "NewManager" || f.Sel.Name == "NewAIManager"
		}
		return false
	}
	held := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for i, rhs := range as.Rhs {
				if i < len(as.Lhs) && isMaker(rhs) {
					if id, ok := as.Lhs[i].(*ast.Ident); ok {
						held[id.Name] = true
					}
				}
			}
		}
		return true
	})
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, isIdent := sel.X.(*ast.Ident)
		if (isMaker(sel.X) || isIdent && held[id.Name]) && !slices.Contains(managerReads, sel.Sel.Name) {
			t.Errorf("%s: %s calls the session Manager's %s, which a draft never does", file, fn.Name.Name, sel.Sel.Name)
		}
		return true
	})
}

// isFuncLike: the app package's exported types are allowed; anything else
// (a function or variable) must be on the list. The types the ai package
// names are listed here.
func isFuncLike(name string) bool {
	return !slices.Contains([]string{"App", "SecretHit", "Pending"}, name)
}

// checkFunc checks the calls in fn on its *app.App parameters and
// variables, and on their Repo field.
func checkFunc(t *testing.T, file, appName string, fn *ast.FuncDecl) {
	if appName == "" || fn.Body == nil {
		return
	}
	apps := map[string]bool{}
	repos := map[string]bool{}
	add := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, f := range fields.List {
			star, ok := f.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, _ := sel.X.(*ast.Ident)
			for _, n := range f.Names {
				switch {
				case pkg != nil && pkg.Name == appName && sel.Sel.Name == "App":
					apps[n.Name] = true
				case pkg != nil && pkg.Name == "gitx" && sel.Sel.Name == "Repo":
					repos[n.Name] = true
				}
			}
		}
	}
	add(fn.Type.Params)
	add(fn.Recv)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch x := sel.X.(type) {
		case *ast.Ident:
			if apps[x.Name] && !slices.Contains(appReads, sel.Sel.Name) {
				t.Errorf("%s: %s calls %s.%s, which is not on the read allowlist", file, fn.Name.Name, x.Name, sel.Sel.Name)
			}
			if repos[x.Name] {
				checkRepoCall(t, file, fn.Name.Name, sel.Sel.Name, call)
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && apps[id.Name] && x.Sel.Name == "Repo" {
				checkRepoCall(t, file, fn.Name.Name, sel.Sel.Name, call)
			}
		}
		return true
	})
}

func checkRepoCall(t *testing.T, file, fn, method string, call *ast.CallExpr) {
	if !slices.Contains(repoReads, method) {
		t.Errorf("%s: %s calls Repo.%s, which is not on the read allowlist", file, fn, method)
		return
	}
	if method != "Git" {
		return
	}
	if len(call.Args) < 2 {
		t.Errorf("%s: %s calls Repo.Git without a subcommand", file, fn)
		return
	}
	arg := call.Args[1]
	// append([]string{"diff", ...}, args...)...: the subcommand is the
	// slice literal's first element.
	if c, isCall := arg.(*ast.CallExpr); isCall && len(c.Args) > 0 && call.Ellipsis.IsValid() {
		if id, isIdent := c.Fun.(*ast.Ident); isIdent && id.Name == "append" {
			if cl, isLit := c.Args[0].(*ast.CompositeLit); isLit && len(cl.Elts) > 0 {
				arg = cl.Elts[0]
			}
		}
	}
	lit, ok := arg.(*ast.BasicLit)
	sub, _ := strconv.Unquote(func() string {
		if ok {
			return lit.Value
		}
		return ""
	}())
	if !ok || !slices.Contains(gitReads, sub) {
		t.Errorf("%s: %s runs git with a subcommand that is not a literal read (%s)", file, fn, sub)
	}
}
