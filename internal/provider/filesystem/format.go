package filesystem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/okf"
	"gopkg.in/yaml.v3"
)

const (
	taskKind     = "task"
	documentKind = "document"
)

type header struct {
	Template   *provider.TaskTemplateRef `yaml:"template,omitempty"`
	Version    int                       `yaml:"version"`
	Kind       string                    `yaml:"kind"`
	Ref        provider.Ref              `yaml:",inline"`
	Title      string                    `yaml:"title"`
	State      *provider.TaskState       `yaml:"state,omitempty"`
	Type       string                    `yaml:"type,omitempty"`
	Priority   string                    `yaml:"priority,omitempty"`
	Assignee   string                    `yaml:"assignee,omitempty"`
	Labels     []string                  `yaml:"labels,omitempty"`
	Links      []provider.Link           `yaml:"links,omitempty"`
	Operations map[string]string         `yaml:"operations,omitempty"`
}

type record struct {
	header
	body        string
	name        string
	revision    string
	mode        fs.FileMode
	concept     okf.Concept
	info        fs.FileInfo
	explicitRef bool
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r record) validate() error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported front matter version %d (expected 1)", r.Version)
	}
	if err := r.Ref.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("title is required")
	}
	switch r.Kind {
	case taskKind:
		if r.Template != nil {
			if err := r.Template.Validate(); err != nil {
				return err
			}
		}
		if r.State == nil || strings.TrimSpace(r.State.ID) == "" {
			return errors.New("task state.id is required")
		}
	case documentKind:
		if r.Template != nil || r.State != nil || r.Type != "" || r.Priority != "" || r.Assignee != "" || len(r.Labels) > 0 {
			return errors.New("document contains task-only fields")
		}
	default:
		return fmt.Errorf("unknown record kind %q", r.Kind)
	}
	for _, link := range r.Links {
		if link.Ref != nil {
			if err := link.Ref.Validate(); err != nil {
				return fmt.Errorf("link reference: %w", err)
			}
		}
	}
	for key, value := range r.Operations {
		if _, err := hex.DecodeString(value); strings.TrimSpace(key) == "" || len(value) != 2*sha256.Size || err != nil {
			return fmt.Errorf("invalid operation receipt for %q", key)
		}
	}
	return nil
}

func parse(data []byte) (record, error) {
	if !utf8.Valid(data) {
		return record{}, errors.New("markdown must be UTF-8")
	}
	first, rest, ok := bytes.Cut(data, []byte("\n"))
	if !ok || string(bytes.TrimSuffix(first, []byte("\r"))) != "---" {
		return record{}, errors.New("expected opening YAML front matter delimiter ---")
	}
	for tail := rest; len(tail) > 0; {
		line, body, _ := bytes.Cut(tail, []byte("\n"))
		if string(bytes.TrimSuffix(line, []byte("\r"))) != "---" {
			tail = body
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(rest[:len(rest)-len(tail)]))
		dec.KnownFields(true)
		r := record{body: string(body), revision: digest(data)}
		if err := dec.Decode(&r.header); err != nil {
			return r, fmt.Errorf("decode YAML front matter: %w", err)
		}
		if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
			return record{}, errors.New("expected a single YAML mapping in front matter")
		}
		return r, r.validate()
	}
	return record{}, errors.New("missing closing YAML front matter delimiter ---")
}

func (r record) markdown() ([]byte, error) {
	if r.concept.Frontmatter != nil {
		return okf.Marshal(r.concept.Frontmatter, r.body)
	}
	h, err := yaml.Marshal(r.header)
	if err != nil {
		return nil, fmt.Errorf("encode YAML front matter: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(h)
	buf.WriteString("---\n")
	buf.WriteString(r.body)
	return buf.Bytes(), nil
}
