package settings

import (
	"context"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
)

// Fields returns the catalog with current values (config.fields), filtered
// by req.
func (s *Service) Fields(ctx context.Context, req FieldsRequest) (View, error) {
	f, err := s.read()
	if err != nil {
		return View{}, err
	}
	ev := s.evaluate(ctx, f.personal.Content, f.repo.Content)
	vals := s.values(ev)
	v := View{CatalogRevision: s.Catalog.Revision(), Revisions: f.revisions(), Groups: consolecfg.Groups(),
		Fields: []config.CatalogField{}, Values: []FieldValue{}, Problems: f.problems(ev.problems)}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	keep := map[valueKey]bool{}
	for _, fd := range s.Catalog.Fields() {
		k := valueKey{fd.ID, fd.Provider}
		fv := vals[k]
		switch {
		case req.Group != "" && fd.Meta.Group != req.Group && !strings.HasPrefix(fd.Meta.Group, req.Group+"."):
			continue
		case len(req.IDs) > 0 && !contains(req.IDs, fd.ID):
			continue
		case query != "" && !strings.Contains(strings.ToLower(fd.ID+"\n"+fd.Meta.Label+"\n"+fd.Meta.Description), query):
			continue
		case req.Modified && !fv.Layer.Set:
			continue
		}
		v.Fields = append(v.Fields, fd)
		keep[k] = true
	}
	for _, fv := range sortedValues(vals) {
		if keep[valueKey{fv.ID, fv.Provider}] {
			v.Values = append(v.Values, fv)
		}
	}
	return v, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// State is what the settings projection reports for the worktree, computed
// from the files and the journal. Values above MaxStateValue are sent as
// TooLarge.
type State struct {
	Revisions       Revisions
	CatalogRevision string
	Values          []FieldValue
	Problems        []Diagnostic
	LastSave        *SaveSummary
	Branch          string
	Protected       bool
}

// State computes the worktree's settings state.
func (s *Service) State(ctx context.Context) (State, error) {
	f, err := s.read()
	if err != nil {
		return State{}, err
	}
	ev := s.evaluate(ctx, f.personal.Content, f.repo.Content)
	st := State{Revisions: f.revisions(), CatalogRevision: s.Catalog.Revision(), Values: sortedValues(s.values(ev)), Problems: f.problems(ev.problems)}
	for i := range st.Values {
		v := &st.Values[i]
		if len(v.Layer.JSON) > MaxStateValue || len(v.Effective.JSON) > MaxStateValue {
			v.TooLarge = true
			v.Layer.JSON, v.Effective.JSON = nil, nil
		}
	}
	if rec, err := s.Journal(ctx); err == nil {
		st.LastSave = s.Summary(ctx, rec)
	} else {
		st.Problems = append(st.Problems, Diagnostic{Severity: SeverityWarning, Edit: -1, Code: CodeConfig, Message: err.Error()})
	}
	st.Branch = s.branch(ctx)
	if s.Repo != nil {
		st.Protected = protectedBranch(st.Branch, f.repo.Content)
	}
	return st, nil
}

// problems adds the personal layer's problem, if any, to the evaluation's.
func (f files) problems(ev []Diagnostic) []Diagnostic {
	out := append([]Diagnostic{}, ev...)
	if f.personalErr != nil {
		out = append(out, Diagnostic{Severity: SeverityWarning, Edit: -1, Code: CodeConfig,
			Message: "Personal settings are not available: " + f.personalErr.Error() + "."})
	}
	return out
}
