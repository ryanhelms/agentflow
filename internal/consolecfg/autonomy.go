package consolecfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"gopkg.in/yaml.v3"
)

// ApprovalSettings contains only editable values. An empty field inherits the
// next layer. Neither the personal file nor unrelated user settings leave disk.
type ApprovalSettings struct {
	Local                  config.Level            `json:"local"`
	Remote                 config.Level            `json:"remote"`
	Window                 string                  `json:"window"`
	Actions                map[string]config.Level `json:"actions"`
	AllowAutoPRPublication *bool                   `json:"allow_auto_pr_publication,omitempty"`
}

func settings(a config.Autonomy, window *time.Duration) ApprovalSettings {
	s := ApprovalSettings{Local: a.Local, Remote: a.Remote, Actions: a.Actions}
	if window != nil {
		s.Window = window.String()
	}
	return s
}

type ApprovalView struct {
	Personal     ApprovalSettings   `json:"personal"`
	Repository   ApprovalSettings   `json:"repository"`
	UserRevision string             `json:"user_revision"`
	Actions      []app.ActionPolicy `json:"actions"`
}

func InspectApprovals(personal, repository []byte) (ApprovalView, error) {
	var user config.User
	var repo config.Repo
	if yaml.Unmarshal(personal, &user) != nil || yaml.Unmarshal(repository, &repo) != nil {
		return ApprovalView{}, errors.New("cannot read approval settings in the configuration file")
	}
	v := ApprovalView{Personal: settings(user.Autonomy, user.ApprovalWindow), Repository: settings(repo.Policy.MaxAutonomy, repo.Policy.MaxApprovalWindow), UserRevision: digest(string(personal))}
	v.Repository.AllowAutoPRPublication = &repo.Policy.AllowAutoPRPublication
	for _, s := range []ApprovalSettings{v.Personal, v.Repository} {
		if err := s.validate(); err != nil {
			return ApprovalView{}, err
		}
	}
	var err error
	v.Actions, err = app.ExplainPolicy(&user, &repo, "")
	return v, err
}

func (s ApprovalSettings) validate() error {
	levels := []config.Level{s.Local, s.Remote}
	for name, level := range s.Actions {
		if !config.ValidAutonomyAction(name) || level == "" {
			return errors.New("choose a supported action and autonomy level")
		}
		levels = append(levels, level)
	}
	for _, level := range levels {
		switch level {
		case "", config.Suggest, config.Confirm, config.Auto:
		default:
			return errors.New("choose suggest, confirm, auto or inherit")
		}
	}
	if s.Window != "" {
		duration, err := time.ParseDuration(s.Window)
		if err != nil || duration < config.MinApprovalWindow || duration > config.MaxApprovalWindow {
			return fmt.Errorf("approval window must be between %s and %s", config.MinApprovalWindow, config.MaxApprovalWindow)
		}
	}
	return nil
}

// EditApprovals changes only the selected approval fields in the original
// document. In particular, saving one preference preserves unrelated settings,
// comments, and action keys containing dots.
func EditApprovals(content []byte, personal bool, next ApprovalSettings) ([]byte, error) {
	if err := next.validate(); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid configuration document")
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected one configuration document")
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	prefix, window := []string{"autonomy"}, []string{"approval_window"}
	var before ApprovalSettings
	var allowedPublication bool
	if personal {
		if next.AllowAutoPRPublication != nil {
			return nil, errors.New("allow_auto_pr_publication is a repository policy setting")
		}
		var u config.User
		if root.Decode(&u) != nil {
			return nil, errors.New("invalid personal configuration")
		}
		before = settings(u.Autonomy, u.ApprovalWindow)
	} else {
		prefix, window = []string{"policy", "max_autonomy"}, []string{"policy", "max_approval_window"}
		var r config.Repo
		if root.Decode(&r) != nil {
			return nil, errors.New("invalid repository configuration")
		}
		before = settings(r.Policy.MaxAutonomy, r.Policy.MaxApprovalWindow)
		allowedPublication = r.Policy.AllowAutoPRPublication
	}
	changed := false
	if next.AllowAutoPRPublication != nil && allowedPublication != *next.AllowAutoPRPublication {
		if err := setApprovalLeaf(root, []string{"policy", "allow_auto_pr_publication"}, "!!bool", strconv.FormatBool(*next.AllowAutoPRPublication)); err != nil {
			return nil, err
		}
		changed = true
	}
	set := func(path []string, old, value string) error {
		if old == value {
			return nil
		}
		changed = true
		return setApprovalLeaf(root, path, "!!str", value)
	}
	if err := set(window, before.Window, next.Window); err != nil {
		return nil, err
	}
	if err := set(append(slices.Clone(prefix), "local"), string(before.Local), string(next.Local)); err != nil {
		return nil, err
	}
	if err := set(append(slices.Clone(prefix), "remote"), string(before.Remote), string(next.Remote)); err != nil {
		return nil, err
	}
	keys := maps.Clone(next.Actions)
	if keys == nil {
		keys = map[string]config.Level{}
	}
	maps.Copy(keys, before.Actions)
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if err := set(append(slices.Clone(prefix), "actions", key), string(before.Actions[key]), string(next.Actions[key])); err != nil {
			return nil, err
		}
	}
	if !changed {
		return content, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func setApprovalLeaf(n *yaml.Node, path []string, tag, value string) error {
	if len(path) == 0 {
		return errors.New("missing approval setting path")
	}
	if err := editableMap(n); err != nil {
		return err
	}
	at := field(n, path[0])
	if at < 0 {
		if value == "" {
			return nil
		}
		n.Content = append(n.Content, keyNode(path[0]), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		at = len(n.Content) - 2
	}
	node := n.Content[at+1]
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("edit anchored approval settings in the source file")
	}
	if len(path) > 1 {
		return setApprovalLeaf(node, path[1:], tag, value)
	}
	if value == "" {
		n.Content = slices.Delete(n.Content, at, at+2)
	} else {
		node.Kind, node.Tag, node.Value, node.Content = yaml.ScalarNode, tag, value, nil
	}
	return nil
}
