package configconsole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/setup"
)

// Candidate fields are shared so a caller can resubmit the exact preview.
type Candidate struct {
	Path         string         `json:"path,omitempty" jsonschema:"Registered configuration path from list"`
	BaseDigest   string         `json:"base_digest,omitempty" jsonschema:"Digest of the on-disk file from read or list"`
	Content      *string        `json:"content,omitempty" jsonschema:"Complete candidate source text; compose can start from disk or an earlier candidate"`
	Title        string         `json:"title,omitempty" jsonschema:"Conventional commit title for repository publication"`
	Mode         string         `json:"mode,omitempty" jsonschema:"Publication mode: draft (default) or direct (apply to the default branch)"`
	TaskRef      string         `json:"task_ref,omitempty" jsonschema:"Optional task reference for repository publication"`
	UserRevision string         `json:"user_revision,omitempty" jsonschema:"Personal settings revision from the autonomy view"`
	Patch        *ApprovalPatch `json:"patch,omitempty" jsonschema:"Change only supplied autonomy fields; an empty string removes that override"`
}

type Query struct {
	Operation string `json:"operation" jsonschema:"list, read, compose, autonomy, stage_autonomy, preview_personal, preview, or readiness"`
	Candidate
	Role    string          `json:"role,omitempty" jsonschema:"Provider role for compose, such as task_management"`
	Edit    string          `json:"edit,omitempty" jsonschema:"compose operation: select a provider, set a setting, or remove its override"`
	Setting string          `json:"setting,omitempty" jsonschema:"Setting path for set or remove"`
	Value   json.RawMessage `json:"value,omitempty" jsonschema:"Typed JSON value for set, or provider identifier for select"`
}

type ApplyRequest struct {
	Operation string `json:"operation" jsonschema:"publish or save_personal; repeat the reviewed fields"`
	Candidate
	ConfirmToken string `json:"confirm_token" jsonschema:"Original confirmation token or an explicitly approved shared operation ID"`
}

type Composition struct {
	Content   string              `json:"content"`
	Providers *setup.ProviderList `json:"providers,omitempty"`
	Warning   string              `json:"warning,omitempty"`
}

// Compose returns a new candidate. A provider switch can require a second edit
// before its required settings resolve; preserve that intermediate source.
func Compose(ctx context.Context, root, base string, content *string, role, operation, setting string, value json.RawMessage) (Composition, error) {
	file, err := CurrentFile(ctx, root, consolecfg.MainFile, base)
	if err != nil {
		return Composition{}, err
	}
	raw := file.Content
	if content != nil {
		raw = *content
	}
	switch operation {
	case "select":
		if setting != "" || len(value) == 0 {
			return Composition{}, errors.New("select requires a provider value and no setting path")
		}
	case "set":
		if setting == "" || len(value) == 0 {
			return Composition{}, errors.New("set requires a setting path and value")
		}
	case "remove":
		if setting == "" {
			return Composition{}, errors.New("remove requires a setting path")
		}
	default:
		return Composition{}, errors.New("choose select, set or remove")
	}
	source, err := consolecfg.EditSetting(raw, role, setting, value, operation == "remove")
	if err != nil {
		return Composition{}, err
	}
	return DescribeComposition(ctx, root, consolecfg.MainFile, source, base), nil
}

// DescribeComposition inspects a staged source, including partially edited documents.
func DescribeComposition(ctx context.Context, root, path, source, base string) Composition {
	result := Composition{Content: source}
	plan, err := consolecfg.Preview(ctx, root, path, source, base)
	if err != nil {
		result.Warning = err.Error()
	} else {
		result.Providers = plan.Providers
	}
	return result
}

func (c Candidate) publication() (Edit, error) {
	if c.Content == nil || c.Path == "" || c.BaseDigest == "" {
		return Edit{}, errors.New("repository publication requires path, base_digest and content")
	}
	if c.Patch != nil || c.UserRevision != "" {
		return Edit{}, errors.New("personal settings cannot be included in a repository publication")
	}
	if c.Mode != "" && c.Mode != "draft" && c.Mode != "direct" {
		return Edit{}, errors.New("mode must be draft or direct")
	}
	return Edit{Path: c.Path, Base: c.BaseDigest, Content: *c.Content, Title: c.Title, Task: c.TaskRef, Draft: c.Mode != "direct"}, nil
}

func (c Candidate) personal(ctx context.Context, a *app.App, token string) (any, error) {
	if c.Patch == nil || c.UserRevision == "" {
		return nil, errors.New("personal changes require patch and user_revision from autonomy")
	}
	if c.Path != "" || c.BaseDigest != "" || c.Content != nil || c.Title != "" || c.Mode != "" || c.TaskRef != "" {
		return nil, errors.New("repository fields cannot be included in a personal settings change")
	}
	return PersonalAutonomy(ctx, a, c.UserRevision, *c.Patch, token)
}

func Inspect(ctx context.Context, a *app.App, in Query) (any, error) {
	root := a.Repo.Dir
	switch in.Operation {
	case "list", "read":
		v, err := consolecfg.View(ctx, root)
		if err != nil {
			return nil, err
		}
		for i, f := range v.Files {
			if in.Operation == "read" && f.Path == in.Path {
				return f, nil
			}
			v.Files[i].Content = ""
		}
		if in.Operation == "read" {
			return nil, errors.New("unknown configuration source; use list to see registered paths")
		}
		return v, nil
	case "readiness":
		return Readiness(ctx, root)
	case "compose", "autonomy", "stage_autonomy":
		if in.Path != "" && in.Path != consolecfg.MainFile {
			return nil, errors.New("provider and autonomy controls use .devflow/config.yml")
		}
		switch in.Operation {
		case "compose":
			return Compose(ctx, root, in.BaseDigest, in.Content, in.Role, in.Edit, in.Setting, in.Value)
		case "stage_autonomy":
			if in.Patch == nil {
				return nil, errors.New("stage_autonomy requires patch")
			}
			return StageAutonomy(ctx, root, in.BaseDigest, in.Content, *in.Patch)
		default:
			return InspectAutonomy(ctx, root, in.BaseDigest, in.Content)
		}
	case "preview":
		edit, err := in.publication()
		if err != nil {
			return nil, err
		}
		return Review(ctx, a.Repo, a.Gate, "agent", edit, "", false, nil)
	case "preview_personal":
		return in.personal(ctx, a, "")
	default:
		return nil, fmt.Errorf("unknown configuration inspection %q", in.Operation)
	}
}

func Apply(ctx context.Context, a *app.App, in ApplyRequest) (any, error) {
	if in.ConfirmToken == "" {
		return nil, errors.New("an exact preview and confirm_token are required to apply")
	}
	switch in.Operation {
	case "publish":
		edit, err := in.publication()
		if err != nil {
			return nil, err
		}
		return Review(ctx, a.Repo, a.Gate, "agent", edit, in.ConfirmToken, true, nil)
	case "save_personal":
		return in.personal(ctx, a, in.ConfirmToken)
	default:
		return nil, fmt.Errorf("unknown configuration write %q", in.Operation)
	}
}
