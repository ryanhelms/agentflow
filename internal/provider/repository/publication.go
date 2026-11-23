package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
)

// Every exit closes the FileSystem handle before removing its checkout.
func withCheckout[T any](ctx context.Context, a adapter, run func(checkout) (T, error)) (result T, err error) {
	root, err := os.MkdirTemp("", "devflow-repository-")
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	c := checkout{root: root, origin: a.Origin, branch: a.Branch, okfKnowledge: a.okfKnowledge}
	if err := c.init(ctx); err != nil {
		return result, err
	}
	return run(c)
}

func withStore[S io.Closer, T any](s snapshot, instance string, open func(string, string) (S, error), run func(S) (T, error)) (result T, err error) {
	store, err := open(instance, s.dir)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	return run(store)
}

func read[S io.Closer, T any](ctx context.Context, a adapter, open func(string, string) (S, error), run func(S) (T, error)) (T, error) {
	return withCheckout(ctx, a, func(c checkout) (T, error) {
		var zero T
		base, err := c.fetch(ctx, c.branch)
		if err != nil {
			return zero, err
		}
		s, err := c.load(ctx, base, a.Directory)
		if err != nil {
			return zero, err
		}
		return withStore(s, a.Instance, open, run)
	})
}

type mutation func(snapshot) (provider.WriteResult, error)

func write[S io.Closer](ctx context.Context, a adapter, kind, scope string, opts provider.WriteOptions, open func(string, string) (S, error), run func(S, provider.WriteOptions) (provider.WriteResult, error)) (provider.WriteResult, error) {
	if opts.OperationID == "" {
		opts.OperationID = rand.Text()
	}
	apply := func(s snapshot) (provider.WriteResult, error) {
		return withStore(s, a.Instance, open, func(store S) (provider.WriteResult, error) { return run(store, opts) })
	}
	return withCheckout(ctx, a, func(c checkout) (provider.WriteResult, error) {
		// Length-delimit fields so opaque IDs cannot alias another operation.
		var key strings.Builder
		for _, field := range []string{c.branch, a.Directory, kind, a.Instance, scope, opts.OperationID} {
			key.WriteString(strconv.Itoa(len(field)))
			key.WriteByte(':')
			key.WriteString(field)
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(key.String())))
		return a.publish(ctx, c, id, opts.OperationID, apply)
	})
}

// At most three pushes. After every failed push (even the last), refetch and
// check the remote receipt. Retry only if a competing update moved the base.
const maxPushes = 3

func (a adapter) publish(ctx context.Context, c checkout, id, op string, apply mutation) (provider.WriteResult, error) {
	var zero provider.WriteResult
	target := c.branch
	review := a.Publication == "pull-request"
	if review {
		target = "devflow/publication/" + id
		if target == c.branch {
			return zero, errors.New("publication branch coincides with target base")
		}
	}
	if a.BeforeWrite != nil {
		if err := c.checkPushOrigin(ctx); err != nil {
			return zero, err
		}
		if err := a.BeforeWrite(c.branch, target); err != nil {
			return zero, err
		}
	}
	var attemptedBase string
	var pushErr error
	for pushes := 0; ; pushes++ {
		base, err := c.fetch(ctx, c.branch)
		if err != nil {
			return zero, errors.Join(pushErr, err)
		}
		s, err := c.load(ctx, base, a.Directory)
		if err != nil {
			return zero, err
		}
		result, applyErr := apply(s)
		if applyErr == nil && result.Status == provider.AlreadyApplied {
			return result, nil
		}
		// An unreadable base cannot validate a review. The compatible reader
		// also cannot use a stale review branch to overwrite an OKF concept.
		if a.okfKnowledge && (errors.Is(applyErr, filesystem.ErrKnowledgeScan) ||
			(a.format == "" && errors.Is(applyErr, provider.ErrUnsupported))) {
			return zero, applyErr
		}
		// An engineer may have removed the record or its receipt after
		// accepting the operation. Its commit still prevents resurrection,
		// even on replay by a different client.
		prior, err := c.git(ctx, nil, "log", "-1", "--format=%H", "--basic-regexp", "--grep=^Devflow-Operation: "+id+"$", base, "--")
		if err != nil {
			return zero, errors.Join(pushErr, err)
		}
		if prior != "" {
			return zero, fmt.Errorf("operation %q already recorded by commit %s: %w", op, strings.TrimSpace(prior), errors.Join(provider.ErrConflict, applyErr))
		}
		// A previous attempt may have published its review branch while the
		// base moved. Verify its receipt before interpreting a stale base.
		if review {
			pending, exists, err := a.existingReview(ctx, c, target, apply)
			if err != nil {
				return zero, errors.Join(pushErr, err)
			}
			if exists {
				return a.requestReview(ctx, c, target, op, pending)
			}
		}
		if applyErr != nil {
			return zero, applyErr
		}
		if pushErr != nil {
			if attemptedBase == base || review {
				return zero, pushErr
			}
			// Retry on top of competing work, including only histories that
			// still contain the snapshot against which we prepared the write.
			if _, err := c.git(ctx, nil, "merge-base", "--is-ancestor", attemptedBase, base); err != nil {
				return zero, fmt.Errorf("history changed while reconciling operation %q: %w", op, errors.Join(provider.ErrConflict, err, pushErr))
			}
			if pushes == maxPushes {
				return zero, fmt.Errorf("base moved during %d push attempts: %w", pushes, errors.Join(provider.ErrConflict, pushErr))
			}
		}
		commit, err := c.commit(ctx, base, a.Directory, id, s, result)
		if err != nil {
			return zero, err
		}
		if err := a.push(c, ctx, commit, target); err != nil {
			attemptedBase = base
			pushErr = fmt.Errorf("push operation %q to branch %q: %w", op, target, err)
			continue
		}
		if review {
			return a.requestReview(ctx, c, target, op, result)
		}
		return result, nil
	}
}

func (a adapter) existingReview(ctx context.Context, c checkout, head string, apply mutation) (provider.WriteResult, bool, error) {
	var zero provider.WriteResult
	out, err := c.git(ctx, nil, "ls-remote", "--refs", "--", c.origin, "refs/heads/"+head)
	if err != nil || out == "" {
		return zero, false, err
	}
	tip, err := c.fetch(ctx, head)
	if err != nil {
		return zero, false, err
	}
	s, err := c.load(ctx, tip, a.Directory)
	if err != nil {
		return zero, false, err
	}
	result, err := apply(s)
	if err != nil {
		return zero, false, err
	}
	if result.Status != provider.AlreadyApplied {
		return zero, false, fmt.Errorf("publication branch %s lacks the expected receipt: %w", head, provider.ErrConflict)
	}
	return result, true, nil
}

func (a adapter) requestReview(ctx context.Context, c checkout, head, op string, r provider.WriteResult) (provider.WriteResult, error) {
	r.Status = provider.PendingReview
	r.Branch = head
	if a.Publisher == nil {
		return r, nil
	}
	url, err := a.Publisher.EnsurePullRequest(ctx, Publication{
		Origin: c.origin, Base: c.branch, Head: head, OperationID: op, Ref: r.Ref,
	})
	if err != nil {
		return r, fmt.Errorf("branch %q is pending for operation %q: %w", head, op, err)
	}
	r.URL = url
	return r, nil
}
