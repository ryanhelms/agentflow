package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	DefaultApprovalWindow = 5 * time.Minute
	MinApprovalWindow     = time.Minute
	MaxApprovalWindow     = time.Hour
)

// AutonomyAction describes a policy key and the classes its operations may use.
// Confirmation describes mandatory rules applied by the action implementation.
type AutonomyAction struct {
	Classes      []string `json:"classes"`
	Confirmation string   `json:"confirmation,omitempty"`
	Mandatory    bool     `json:"mandatory"`
}

// A provider can implement an operation through either a local file or a remote
// write. Resolution always uses the class of the actual operation.
var autonomyCatalog = map[string]struct{ classes, confirmation string }{
	"adr_draft":             {"local", ""},
	"comment":               {"remote", "Some provider routes require confirmation."},
	"commit":                {"local", ""},
	"console.autonomy":      {"local", "Agent changes to personal approval settings require confirmation."},
	"console.publish":       {"remote", "Repository console publication requires confirmation."},
	"console.reply":         {"local", "Repository console replies require confirmation."},
	"docs_publish":          {"local remote", "Publication can require confirmation for the selected provider route."},
	"eod":                   {"remote", ""},
	"jira_remote_link":      {"remote", ""},
	"notify":                {"remote", ""},
	"pr_open":               {"remote", ""},
	"pr_open_draft":         {"remote", ""},
	"pr_ready_for_review":   {"remote", ""},
	"push":                  {"remote", ""},
	"release_publish":       {"remote", "Release publication requires confirmation."},
	"review_reply":          {"remote", ""},
	"setup.install_gh":      {"local", "Installation requires confirmation."},
	"setup.install_mcp":     {"local", "Installation requires confirmation."},
	"setup.register_client": {"local", "Registration requires confirmation."},
	"setup.register_mcp":    {"local", "Registration requires confirmation."},
	"start":                 {"local remote", "Some provider routes require confirmation."},
	"ticket_create":         {"local remote", "Task creation requires confirmation."},
	"transition":            {"local remote", "Some provider routes require confirmation."},
}

func AutonomyActions() []string { return slices.Sorted(maps.Keys(autonomyCatalog)) }

func ValidAutonomyAction(action string) bool {
	_, ok := autonomyCatalog[action]
	return ok
}

func DescribeAutonomyAction(action string) (AutonomyAction, bool) {
	v, ok := autonomyCatalog[action]
	required := strings.HasPrefix(action, "setup.") || strings.HasPrefix(action, "console.") || action == "release_publish" || action == "ticket_create"
	return AutonomyAction{Classes: strings.Fields(v.classes), Confirmation: v.confirmation, Mandatory: ok && required}, ok
}

func validateAutonomy(a Autonomy, path string, window *time.Duration, windowPath string) error {
	for _, key := range slices.Sorted(maps.Keys(a.Actions)) {
		if !ValidAutonomyAction(key) {
			return fmt.Errorf("%s.actions: unknown action %q", path, key)
		}
		switch a.Actions[key] {
		case Suggest, Confirm, Auto:
		default:
			return fmt.Errorf("%s.actions.%s: expected suggest, confirm or auto, got %q", path, key, a.Actions[key])
		}
	}
	if window != nil && (*window < MinApprovalWindow || *window > MaxApprovalWindow) {
		return fmt.Errorf("%s: duration must be between 1m and 1h", windowPath)
	}
	return nil
}

// AutonomyDecision records both input layers and which rule determined Level.
type AutonomyDecision struct {
	Class            string        `json:"class"`
	Action           string        `json:"action"`
	Level            Level         `json:"level"`
	Source           string        `json:"source"`
	UserClass        Level         `json:"user_class"`
	UserClassDefault bool          `json:"user_class_default"`
	UserAction       Level         `json:"user_action,omitempty"`
	RepoClass        Level         `json:"repo_class"`
	RepoClassDefault bool          `json:"repo_class_default"`
	RepoAction       Level         `json:"repo_action,omitempty"`
	ForceConfirm     bool          `json:"force_confirm"`
	Window           time.Duration `json:"-"`
	ApprovalWindow   string        `json:"approval_window"`
	WindowSource     string        `json:"window_source"`
}

// ResolveAutonomy selects a personal preference for the exact action and applies
// both repository ceilings. forceConfirm preserves mandatory review for a
// particular operation or provider route. Nil configs use the loader defaults.
func ResolveAutonomy(user *User, repo *Repo, class, action string, forceConfirm bool) AutonomyDecision {
	if user == nil {
		user = &User{}
	}
	if repo == nil {
		repo = &Repo{}
	}
	d := AutonomyDecision{Class: class, Action: action, ForceConfirm: forceConfirm, Window: DefaultApprovalWindow, WindowSource: "default"}
	if u := user.ApprovalWindow; u != nil {
		d.Window, d.WindowSource = *u, "approval_window"
	}
	if r := repo.Policy.MaxApprovalWindow; r != nil && *r < d.Window {
		d.Window, d.WindowSource = *r, "policy.max_approval_window"
	}
	d.ApprovalWindow = d.Window.String()
	if class == "read" {
		d.Level, d.Source = Auto, "read"
		return d
	}
	if class != "local" && class != "remote" {
		d.Level, d.Source = Confirm, "unknown class"
		return d
	}
	d.UserClass, d.UserClassDefault = user.Autonomy.classValue(class, Confirm)
	d.RepoClass, d.RepoClassDefault = repo.Policy.MaxAutonomy.classValue(class, Auto)
	d.Level, d.Source = d.UserClass, "autonomy."+class
	if d.UserClassDefault {
		d.Source = "default"
	}
	k, known := DescribeAutonomyAction(action)
	if known && slices.Contains(k.Classes, class) {
		d.UserAction = user.Autonomy.Actions[action]
		d.RepoAction = repo.Policy.MaxAutonomy.Actions[action]
		if d.UserAction != "" {
			d.Level, d.Source = d.UserAction, "autonomy.actions."+action
		}
	}
	if d.RepoClass.rank() < d.Level.rank() {
		d.Level, d.Source = d.RepoClass, "policy.max_autonomy."+class
	}
	if d.RepoAction != "" && d.RepoAction.rank() < d.Level.rank() {
		d.Level, d.Source = d.RepoAction, "policy.max_autonomy.actions."+action
	}
	if forceConfirm && d.Level == Auto {
		d.Level, d.Source = Confirm, "mandatory confirmation"
	}
	return d
}

func (a Autonomy) classValue(class string, fallback Level) (Level, bool) {
	level, defaulted := a.Local, a.defaultLocal
	if class == "remote" {
		level, defaulted = a.Remote, a.defaultRemote
	}
	switch level {
	case "":
		return fallback, true
	case Auto, Suggest, Confirm:
		return level, defaulted
	default:
		// Legacy class settings treat unrecognized levels as confirm.
		return Confirm, false
	}
}
