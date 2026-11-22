package filesystem

import (
	"context"
	"errors"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

// Knowledge manages document records in a caller-owned directory.
type Knowledge struct{ *store }

var _ provider.Knowledge = (*Knowledge)(nil)

// NewKnowledge opens an existing directory. Close the adapter when finished.
func NewKnowledge(instance, directory string) (*Knowledge, error) {
	s, err := openStore(instance, directory, documentKind)
	if err != nil {
		return nil, err
	}
	return &Knowledge{s}, nil
}

func document(r record) provider.Document {
	return provider.Document{Ref: r.Ref, Title: r.Title, Body: r.body, Links: r.Links, Revision: r.revision}
}

func (s *Knowledge) Get(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	r, err := s.get(ctx, ref)
	if err != nil {
		return provider.Document{}, err
	}
	return document(r), nil
}

func (s *Knowledge) Find(ctx context.Context, q provider.DocumentQuery) ([]provider.Document, error) {
	if q.Limit < 0 {
		return nil, errors.New("document query limit must be nonnegative")
	}
	rs, err := s.scan(ctx, q.Scope)
	if err != nil {
		return nil, err
	}
	var result []provider.Document
	text := strings.ToLower(q.Text)
	for _, r := range rs {
		if matches(r, text) {
			result = append(result, document(r))
			if len(result) == q.Limit {
				break
			}
		}
	}
	return result, nil
}

func (s *Knowledge) Publish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	r := record{header: header{
		Version: 1, Kind: documentKind, Ref: ref, Title: draft.Title, Links: draft.Links,
	}, body: draft.Body}
	return s.write(ctx, publish, r, opts)
}
