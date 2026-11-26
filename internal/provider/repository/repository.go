// Package repository serves FileSystem task and knowledge records from a Git branch.
// Each call fetches into a private temporary repository. Only the configured
// directory's Markdown blobs are checked out, byte for byte. FileSystem handles
// are closed and the checkout is removed before the call returns.
//
// Reads always use the base branch. In push mode concurrent unrelated changes
// can be retried up to three total pushes. In pull-request mode the branch
// name derives from the base, directory, kind, instance, scope and operation
// ID. A nil Publisher leaves the branch pending and returns PendingReview
// with an empty URL. Supply a stable WriteOptions.OperationID for replay
// across calls. An omitted ID is generated once per call.
package repository

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/filesystem"
	"github.com/ryanhelms/agentflow/internal/provider/okf"
)

// Config takes a resolved origin URL or absolute repository path. The caller
// resolves an omitted repository setting from the engineer's origin. Branch
// defaults to the remote HEAD. Directory is relative to that branch's root.
type Config struct {
	Instance    string
	Origin      string
	Branch      string
	Directory   string
	Publication string // "push" or "pull-request" (default)
	Publisher   Publisher
	// BeforeWrite checks the resolved base and actual push target before any
	// record mutation, push or PR reconciliation. Reads never invoke it.
	BeforeWrite func(base, target string) error
}

// Publisher ensures a change request exists for Head targeting Base. Implementations
// must look up/reuse the same change on replay, including after an ambiguous API error.
type Publisher interface {
	EnsurePullRequest(context.Context, Publication) (url string, err error)
}

type Publication struct {
	Origin, Base, Head, OperationID string
	Ref                             provider.Ref
}

type adapter struct {
	Config
	okfKnowledge bool
	format       string
	// Tests exercise transport failures and competing pushes at the publication boundary.
	push func(checkout, context.Context, string, string) error
}

type Tasks struct{ a adapter }
type Knowledge struct{ a adapter }

var (
	_ provider.Tasks     = (*Tasks)(nil)
	_ provider.Knowledge = (*Knowledge)(nil)
)

func NewTasks(c Config) (*Tasks, error) {
	a, err := newAdapter(c)
	if err != nil {
		return nil, err
	}
	return &Tasks{a}, nil
}

func NewKnowledge(c Config) (*Knowledge, error) {
	a, err := newAdapter(c)
	if err != nil {
		return nil, err
	}
	return &Knowledge{a: a}, nil
}

// NewOKFCompatibleKnowledge enables recursive OKF reads for the Repository
// knowledge role. Wiki uses NewKnowledge and its flat v1 checkout.
func NewOKFCompatibleKnowledge(c Config) (*Knowledge, error) {
	k, err := NewKnowledge(c)
	if err == nil {
		k.a.okfKnowledge = true
	}
	return k, err
}

// NewOKFKnowledge publishes new documents in format and preserves the encoding
// of existing records, including imported OKF concepts in nested directories.
func NewOKFKnowledge(cfg Config, format string) (*Knowledge, error) {
	if err := okf.ValidateFormat(format); err != nil {
		return nil, err
	}
	k, err := NewOKFCompatibleKnowledge(cfg)
	if err == nil {
		k.a.format = format
	}
	return k, err
}

func (k *Knowledge) open(instance, dir string) (*filesystem.Knowledge, error) {
	if k.a.format != "" {
		return filesystem.NewOKFKnowledge(instance, dir, k.a.format)
	}
	if k.a.okfKnowledge {
		return filesystem.NewOKFCompatibleKnowledge(instance, dir)
	}
	return filesystem.NewKnowledge(instance, dir)
}

func newAdapter(c Config) (adapter, error) {
	if err := (provider.Ref{Instance: c.Instance, Scope: "_", ID: "_"}).Validate(); err != nil {
		return adapter{}, err
	}
	if c.Origin == "" || strings.HasPrefix(c.Origin, "-") || strings.ContainsFunc(c.Origin, unicode.IsControl) {
		return adapter{}, errors.New("invalid repository origin")
	}
	if !filepath.IsAbs(c.Origin) && !strings.HasPrefix(c.Origin, "file://") {
		if scheme, _, helper := strings.Cut(c.Origin, "::"); helper && !strings.ContainsAny(scheme, "/[:") {
			return adapter{}, errors.New("git remote helpers are unsupported")
		}
		if _, err := gitx.ParseRemoteURL(c.Origin); err != nil {
			return adapter{}, fmt.Errorf("expected an origin URL or absolute path: %w", err)
		}
	}
	if !fs.ValidPath(c.Directory) || strings.ContainsAny(c.Directory, "\\:") {
		return adapter{}, errors.New("expected a slash-separated checkout-relative directory")
	}
	for _, part := range strings.Split(c.Directory, "/") {
		if part != "." && !safeComponent(part) {
			return adapter{}, fmt.Errorf("invalid repository directory component %q", part)
		}
	}
	if c.Publication == "" {
		c.Publication = "pull-request"
	}
	if c.Publication != "pull-request" && c.Publication != "push" {
		return adapter{}, fmt.Errorf("unknown repository publication mode %q", c.Publication)
	}
	return adapter{Config: c, push: checkout.push}, nil
}

// Components are also passed to Git's index. Exclude metadata names and
// platform-specific aliases even on case-sensitive filesystems.
func safeComponent(s string) bool {
	return filepath.IsLocal(s) && !strings.ContainsAny(s, "/\\:") &&
		!strings.ContainsFunc(s, unicode.IsControl) && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, " ") &&
		!strings.EqualFold(s, ".git") && !strings.EqualFold(s, "git~1")
}

func (t *Tasks) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	return read(ctx, t.a, filesystem.NewTasks, func(s *filesystem.Tasks) (provider.Task, error) {
		return s.Get(ctx, ref)
	})
}

func (t *Tasks) Find(ctx context.Context, q provider.TaskQuery) ([]provider.Task, error) {
	return read(ctx, t.a, filesystem.NewTasks, func(s *filesystem.Tasks) ([]provider.Task, error) {
		return s.Find(ctx, q)
	})
}

func (t *Tasks) Create(ctx context.Context, d provider.TaskDraft, o provider.WriteOptions) (provider.WriteResult, error) {
	return write(ctx, t.a, "task", d.Scope, o, filesystem.NewTasks, func(s *filesystem.Tasks, opts provider.WriteOptions) (provider.WriteResult, error) {
		return s.Create(ctx, d, opts)
	})
}

func (t *Tasks) Transition(ctx context.Context, ref provider.Ref, state provider.TaskState, o provider.WriteOptions) (provider.WriteResult, error) {
	// Pin even an unconditional transition to the record we first observed.
	// A competing edit during this call requires the caller to reload.
	expected := o.ExpectedRevision
	return write(ctx, t.a, "task", ref.Scope, o, filesystem.NewTasks, func(s *filesystem.Tasks, opts provider.WriteOptions) (provider.WriteResult, error) {
		if expected == "" {
			task, err := s.Get(ctx, ref)
			if err != nil {
				return provider.WriteResult{}, err
			}
			expected = task.Revision
		}
		opts.ExpectedRevision = expected
		return s.Transition(ctx, ref, state, opts)
	})
}

func (k *Knowledge) Get(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	return read(ctx, k.a, k.open, func(s *filesystem.Knowledge) (provider.Document, error) {
		return s.Get(ctx, ref)
	})
}

func (k *Knowledge) Find(ctx context.Context, q provider.DocumentQuery) ([]provider.Document, error) {
	return read(ctx, k.a, k.open, func(s *filesystem.Knowledge) ([]provider.Document, error) {
		return s.Find(ctx, q)
	})
}

func (k *Knowledge) Publish(ctx context.Context, ref provider.Ref, d provider.DocumentDraft, o provider.WriteOptions) (provider.WriteResult, error) {
	return write(ctx, k.a, "document", ref.Scope, o, k.open, func(s *filesystem.Knowledge, opts provider.WriteOptions) (provider.WriteResult, error) {
		return s.Publish(ctx, ref, d, opts)
	})
}
