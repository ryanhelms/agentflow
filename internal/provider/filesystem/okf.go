package filesystem

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/okf"
	"gopkg.in/yaml.v3"
)

// ErrKnowledgeScan means discovery failed before the store could establish a
// unique write destination. A Repository publisher must not reconcile a pending
// review from an older snapshot when it cannot validate the current base.
var ErrKnowledgeScan = errors.New("cannot scan knowledge directory")

// IsKnowledgeCandidate selects files to inspect in a compatible knowledge store.
// name is slash-separated and relative to its root. The v1 reader allowed any
// top-level .md spelling, including renamed records at OKF-reserved names.
func IsKnowledgeCandidate(name string) bool {
	if !strings.Contains(name, "/") {
		return strings.EqualFold(path.Ext(name), ".md")
	}
	return isOKFPath(name)
}

func isOKFPath(name string) bool {
	base := path.Base(name)
	return strings.HasSuffix(base, ".md") && base != "index.md" && base != "log.md" &&
		!strings.HasPrefix(name, ".") && !strings.Contains(name, "/.")
}

func parseKnowledge(instance, name string, data []byte) (record, error) {
	legacyErr := okf.ErrNotConcept
	if !strings.Contains(name, "/") {
		// Recognize v1 first: a legacy task may itself have a nonempty type.
		r, err := parse(data)
		if err == nil {
			return r, nil
		}
		// A damaged v1 record must not silently disappear and allow another
		// publish under its identity. Ordinary Markdown can also have metadata.
		identified := r.Ref.Instance != "" || (r.Ref.Scope != "" && r.Ref.ID != "")
		if identified && (r.Kind == documentKind || r.Kind == taskKind || r.Version != 0) {
			legacyErr = err
		}
	}
	if !isOKFPath(name) {
		return record{}, legacyErr
	}
	concept, err := okf.Parse(data)
	if errors.Is(err, okf.ErrNotConcept) {
		return record{}, legacyErr
	}
	if err != nil {
		return record{}, err
	}
	// Ref.ID is opaque and Ref.String already escapes embedded slashes.
	// Keep the full path so a root file and a file in default/ stay distinct.
	id := strings.TrimSuffix(name, ".md")
	scope, _, nested := strings.Cut(name, "/")
	if !nested {
		scope = "default"
	}
	r := record{
		header: header{Version: 1, Kind: documentKind, Title: concept.Title,
			Ref: provider.Ref{Instance: instance, Scope: scope, ID: id}},
		body: concept.Body, revision: digest(data), concept: concept,
	}
	if strings.TrimSpace(r.Title) == "" {
		r.Title = path.Base(name)
	}
	pairs := concept.Frontmatter.Content
	for i := 0; i < len(pairs); i += 2 {
		key, value := yamlValue(pairs[i]), yamlValue(pairs[i+1])
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			continue
		}
		switch key.Value {
		case "x-agentflow-ref":
			fields, err := extensionStrings(value)
			if err != nil {
				return record{}, fmt.Errorf("x-agentflow-ref: %w", err)
			}
			if len(fields) != 2 || fields["scope"] == "" || fields["id"] == "" {
				return record{}, errors.New("x-agentflow-ref requires exactly scope and id")
			}
			r.Ref.Scope, r.Ref.ID = fields["scope"], fields["id"]
			r.explicitRef = true
		case "x-agentflow-links":
			if value.Kind != yaml.SequenceNode || value.ShortTag() != "!!seq" {
				return record{}, errors.New("x-agentflow-links requires a sequence")
			}
			if err := value.Decode(&r.Links); err != nil {
				return record{}, fmt.Errorf("x-agentflow-links: %w", err)
			}
		case "x-agentflow-operations":
			r.Operations, err = extensionStrings(value)
			if err != nil {
				return record{}, fmt.Errorf("x-agentflow-operations: %w", err)
			}
		}
	}
	return r, r.validate()
}

func extensionStrings(node *yaml.Node) (map[string]string, error) {
	if node.Kind != yaml.MappingNode || node.ShortTag() != "!!map" {
		return nil, errors.New("expected a mapping")
	}
	values := make(map[string]string, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		k, v := yamlValue(node.Content[i]), yamlValue(node.Content[i+1])
		if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" || v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
			return nil, errors.New("expected string keys and values")
		}
		values[k.Value] = v.Value
	}
	return values, nil
}

// yaml.v3 aliases point directly to their anchored value.
func yamlValue(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}
