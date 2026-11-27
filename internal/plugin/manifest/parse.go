package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes a manifest strictly: an unknown key, a wrong type or a
// second YAML document is a problem with code syntax, and the result is a
// *ValidationError. It checks nothing else. Validate does.
//
// The decoder reads plain data only. Nothing in a manifest is executed,
// fetched or included.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > MaxManifestBytes {
		return m, oneProblem("", CodeLimit, fmt.Sprintf("the manifest is larger than %d KiB", MaxManifestBytes>>10))
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, oneProblem("", CodeSyntax, "the manifest is empty")
		}
		return Manifest{}, syntaxProblems(err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, oneProblem("", CodeSyntax, "the manifest holds more than one YAML document")
	}
	return m, nil
}

// Load parses data and validates the manifest: what plugin add and the host
// do with a manifest file. Any problem is a *ValidationError.
func Load(data []byte) (Manifest, error) {
	m, err := Parse(data)
	if err != nil {
		return Manifest{}, err
	}
	if err := Validate(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func oneProblem(path, code, msg string) *ValidationError {
	return &ValidationError{Problems: []Problem{{Path: path, Code: code, Message: msg}}}
}

var (
	unknownKeyRe = regexp.MustCompile(`field (\S+) not found in type \S+`)
	typeRe       = regexp.MustCompile(` in type \S+`)
)

// syntaxProblems turns a yaml.v3 error into problems without Go type names.
func syntaxProblems(err error) *ValidationError {
	var te *yaml.TypeError
	var lines []string
	if errors.As(err, &te) {
		lines = te.Errors
	} else {
		lines = []string{strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	v := &ValidationError{}
	for _, l := range lines {
		l = strings.TrimPrefix(l, "yaml: ")
		l = unknownKeyRe.ReplaceAllString(l, "unknown key $1")
		l = typeRe.ReplaceAllString(l, "")
		v.Problems = append(v.Problems, Problem{Code: CodeSyntax, Message: l})
	}
	if len(v.Problems) == 0 {
		v.Problems = []Problem{{Code: CodeSyntax, Message: "the manifest is not valid YAML"}}
	}
	return v
}
