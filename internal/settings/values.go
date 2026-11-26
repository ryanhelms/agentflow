package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
)

// evaluated is what the two layer files resolve to.
type evaluated struct {
	personal, repo string
	user           *config.User
	cfg            *config.Repo
	problems       []Diagnostic
	userPlain      any
	repoPlain      any
}

// evaluate parses both layers: the personal file as config.LoadUser does, the
// repository file with the worktree's provider definitions as
// config.InspectRepo does. A file that does not load becomes a problem.
func (s *Service) evaluate(ctx context.Context, personal, repo string) evaluated {
	ev := evaluated{personal: personal, repo: repo}
	user, err := parseUser(personal)
	if err != nil {
		ev.problems = append(ev.problems, Diagnostic{Severity: SeverityError, Edit: -1, Code: CodeConfig, Message: PersonalFile + ": " + safeMessage(err)})
		user = &config.User{}
	}
	ev.user = user
	ev.userPlain, _ = consolecfg.Plain(userDefaults(*user))
	if s.Root != "" {
		var defs map[string][]byte
		defs, err = consolecfg.DefinitionSources(ctx, s.Root)
		if err == nil {
			var main []byte
			if repo != "" {
				main = []byte(repo)
			}
			ev.cfg, _, err = config.InspectRepo(main, defs)
		}
		if err != nil {
			ev.problems = append(ev.problems, Diagnostic{Severity: SeverityError, Edit: -1, Code: CodeConfig, Message: safeMessage(err)})
			ev.cfg, _, _ = config.ParseRepoCore([]byte(repo))
		}
	}
	if ev.cfg == nil {
		ev.cfg, _, _ = config.ParseRepoCore(nil)
	}
	ev.repoPlain, _ = consolecfg.Plain(ev.cfg)
	return ev
}

// parseUser reads the personal layer like config.LoadUser, from bytes.
func parseUser(content string) (*config.User, error) {
	u := &config.User{}
	if strings.TrimSpace(content) != "" {
		if err := yaml.Unmarshal([]byte(content), u); err != nil {
			return nil, err
		}
	}
	// InspectApprovals applies LoadUser's level and window checks.
	if _, err := consolecfg.InspectApprovals([]byte(content), nil); err != nil {
		return nil, err
	}
	return u, nil
}

// userDefaults fills the defaults config.LoadUser applies.
func userDefaults(u config.User) config.User {
	if u.Autonomy.Local == "" {
		u.Autonomy.Local = config.Confirm
	}
	if u.Autonomy.Remote == "" {
		u.Autonomy.Remote = config.Confirm
	}
	if u.Init.Auto == "" {
		u.Init.Auto = "auto"
	}
	if len(u.Init.Orgs) == 0 {
		u.Init.Orgs = config.DefaultInitOrgs
	}
	return u
}

// safeMessage is an error's text for a Diagnostic: never a credential-like
// value or an absolute path.
func safeMessage(err error) string {
	msg := err.Error()
	if consolecfg.Sensitive(msg) {
		return "the configuration holds a credential-like value; inspect it locally"
	}
	if strings.Contains(msg, "/Users/") || strings.Contains(msg, "/home/") || strings.Contains(msg, `:\`) {
		return "the configuration does not load; inspect it locally"
	}
	return msg
}

// roleOf returns the provider role a repository path belongs to and its key
// count, or "" for a core field.
func roleOf(keys []string) (string, int) {
	for _, r := range config.ProviderRoles {
		rk := strings.Split(r, ".")
		if isPrefix(rk, keys) {
			return r, len(rk)
		}
	}
	return "", 0
}

// selection returns cfg's selection of role.
func selection(cfg *config.Repo, role string) config.Selection {
	p := cfg.Providers
	switch role {
	case "code_hosting":
		return p.CodeHosting
	case "task_management":
		return p.TaskManagement
	case "knowledge_base":
		return p.KnowledgeBase
	case "devops.checks":
		return p.DevOps.Checks
	case "devops.deployments":
		return p.DevOps.Deployments
	case "devops.releases":
		return p.DevOps.Releases
	case "secops":
		return p.SecOps
	case "notifications":
		return p.Notifications
	}
	return config.Selection{}
}

// selectedProvider is the provider a role selects in repo content, as far as
// the file says: its provider key, or the role's default.
func selectedProvider(content, role string) string {
	keys := append(strings.Split(role, "."), "provider")
	if raw, ok, _ := consolecfg.LayerValue(content, keys); ok {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			return id
		}
	}
	for _, r := range config.ProviderCatalog() {
		if r.Role == role {
			return r.Default
		}
	}
	return ""
}

// value builds a Value from JSON, redacting a sensitive one.
func value(raw json.RawMessage, set, sensitive bool) Value {
	if !set {
		return Value{}
	}
	raw = canonical(raw)
	if sensitive {
		sum := sha256.Sum256(raw)
		return Value{Set: true, Redacted: true, Digest: "sha256:" + hex.EncodeToString(sum[:])}
	}
	return Value{JSON: raw, Set: true}
}

// canonical re-encodes JSON so equal values compare equal byte for byte.
func canonical(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func sameValue(a, b Value) bool {
	return a.Set == b.Set && bytes.Equal(a.JSON, b.JSON) && a.Digest == b.Digest
}

type valueKey struct{ id, provider string }

// values resolves every catalog field: its own layer value, the effective
// value and where it comes from.
func (s *Service) values(ev evaluated) map[valueKey]FieldValue {
	out := map[valueKey]FieldValue{}
	for _, f := range s.Catalog.Fields() {
		keys, err := config.ParseFieldPath(f.Path)
		if err != nil || containsWildcard(keys) {
			continue
		}
		content := ev.personal
		if f.Layer == config.FieldRepo {
			content = ev.repo
		}
		raw, set, _ := consolecfg.LayerValue(content, keys)
		fv := FieldValue{ID: f.ID, Provider: f.Provider, Layer: value(raw, set, f.Meta.Sensitive), Active: true, Source: SourceDefault}
		if set {
			fv.Source = Source(f.Layer)
		}
		eff, effSet := raw, set
		switch f.Layer {
		case config.FieldPersonal:
			eff, effSet = consolecfg.PlainValue(ev.userPlain, keys)
			s.capPersonal(ev, keys, &fv, &eff)
		default:
			role, n := roleOf(keys)
			switch {
			case role == "":
				eff, effSet = consolecfg.PlainValue(ev.repoPlain, keys)
			case len(keys) == n+1 && keys[n] == "provider":
				sel := selection(ev.cfg, role)
				if sel.Provider != "" {
					eff, _ = json.Marshal(sel.Provider)
					effSet = true
				}
			case len(keys) > n && keys[n] == "settings" && f.Provider != "":
				sel := selection(ev.cfg, role)
				fv.Active = sel.Provider == f.Provider
				if fv.Active {
					if plain, err := consolecfg.Plain(sel.Settings); err == nil {
						eff, effSet = consolecfg.PlainValue(plain, keys[n+1:])
					}
					if src, ok := sel.Sources[strings.Join(keys[n+1:], ".")]; ok {
						fv.Source = sourceOf(src)
					}
				}
			case role == "task_management" && len(keys) > n && keys[n] == "templates":
				if t := ev.cfg.Providers.TaskTemplates; t != nil {
					if plain, err := consolecfg.Plain(t); err == nil {
						eff, effSet = consolecfg.PlainValue(plain, keys[n+1:])
					}
				}
			}
		}
		fv.Effective = value(eff, effSet, f.Meta.Sensitive)
		out[valueKey{f.ID, f.Provider}] = fv
	}
	return out
}

func containsWildcard(keys []string) bool {
	for _, k := range keys {
		if k == config.FieldWildcard {
			return true
		}
	}
	return false
}

func sourceOf(src config.Source) Source {
	switch {
	case src.Layer == config.LayerDefault:
		return SourceDefault
	case src.File == RepoFile:
		return SourceRepo
	default:
		return SourceDefinition
	}
}

// capPersonal lowers an effective personal level to the repository ceiling,
// as config.Effective does, and names the ceiling.
func (s *Service) capPersonal(ev evaluated, keys []string, fv *FieldValue, eff *json.RawMessage) {
	if len(keys) != 2 || keys[0] != "autonomy" || (keys[1] != "local" && keys[1] != "remote") {
		return
	}
	user := userDefaults(*ev.user)
	level, ceiling := user.Autonomy.Local, ev.cfg.Policy.MaxAutonomy.Local
	if keys[1] == "remote" {
		level, ceiling = user.Autonomy.Remote, ev.cfg.Policy.MaxAutonomy.Remote
	}
	if got := config.Min(level, ceiling); got != level {
		*eff, _ = json.Marshal(got)
		fv.CappedBy = config.FieldID(config.FieldRepo, "policy.max_autonomy."+keys[1])
	}
}

// sortedValues lists values that are set in a layer or active, sorted by ID,
// then provider.
func sortedValues(m map[valueKey]FieldValue) []FieldValue {
	out := make([]FieldValue, 0, len(m))
	for _, v := range m {
		if v.Active || v.Layer.Set {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

var errNoRepository = errors.New("repository settings need a repository")
