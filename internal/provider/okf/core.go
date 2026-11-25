package okf

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML 1.2.2 §10.3.2. yaml.v3 supplies the syntax tree but uses a different
// implicit schema (including timestamps, binary integers and merge keys).
var (
	coreInteger = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	coreFloat   = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
)

func coreTag(value string) string {
	switch value {
	case "", "~", "null", "Null", "NULL":
		return "!!null"
	case "true", "True", "TRUE", "false", "False", "FALSE":
		return "!!bool"
	case ".nan", ".NaN", ".NAN", ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF":
		return "!!float"
	}
	if coreInteger.MatchString(value) {
		return "!!int"
	}
	if coreFloat.MatchString(value) {
		return "!!float"
	}
	return "!!str"
}

func resolveCore(n *yaml.Node, source sourceLines) {
	if n.Kind == yaml.ScalarNode && n.Style == 0 {
		n.Tag = coreTag(n.Value)
		if n.Tag != "!!str" && source.nonSpecific(n) {
			n.Tag = "!!str"
		}
	}
	for _, child := range n.Content {
		resolveCore(child, source)
	}
}

// keyTable assigns equal keys the same ID. Composite keys refer to child IDs
// so shared anchors need never be expanded into copies of their subtrees.
type keyTable struct {
	ids    map[*yaml.Node]int
	values map[keyValue]int
}

type keyValue struct {
	kind         yaml.Kind
	tag, content string
}

func (t *keyTable) check(n *yaml.Node) error {
	for _, child := range n.Content {
		if err := t.check(child); err != nil {
			return err
		}
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	seen := make(map[int]int, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i]
		id, err := t.keyID(key)
		if err != nil {
			return err
		}
		if previous, ok := seen[id]; ok {
			return fmt.Errorf("duplicate frontmatter key on line %d (first on line %d)", key.Line+1, previous)
		}
		seen[id] = key.Line + 1
	}
	return nil
}

func (t *keyTable) keyID(n *yaml.Node) (int, error) {
	n = dereference(n)
	if id, ok := t.ids[n]; ok {
		if id == 0 {
			return 0, fmt.Errorf("recursive YAML key on line %d", n.Line+1)
		}
		return id, nil
	}
	t.ids[n] = 0
	var value string
	if n.Kind == yaml.ScalarNode {
		value = canonicalScalar(n)
	} else {
		children := make([]string, len(n.Content))
		for i, child := range n.Content {
			id, err := t.keyID(child)
			if err != nil {
				return 0, err
			}
			children[i] = strconv.Itoa(id)
		}
		if n.Kind == yaml.MappingNode {
			pairs := make([]string, 0, len(children)/2)
			for i := 0; i < len(children); i += 2 {
				pairs = append(pairs, children[i]+":"+children[i+1])
			}
			slices.Sort(pairs)
			children = pairs
		}
		value = strings.Join(children, ",")
	}
	key := keyValue{n.Kind, n.Tag, value}
	id := t.values[key]
	if id == 0 {
		id = len(t.values) + 1
		t.values[key] = id
	}
	t.ids[n] = id
	return id, nil
}

func canonicalScalar(n *yaml.Node) string {
	switch n.Tag {
	case "!!null":
		return ""
	case "!!bool":
		return strings.ToLower(n.Value)
	case "!!int":
		base := 10
		value := n.Value
		switch {
		case strings.HasPrefix(value, "0o"):
			base, value = 8, value[2:]
		case strings.HasPrefix(value, "0x"):
			base, value = 16, value[2:]
		}
		if i, ok := new(big.Int).SetString(value, base); ok {
			return i.String()
		}
	case "!!float":
		return canonicalFloat(n.Value)
	}
	return n.Value
}

// Compare decimal keys without float64 rounding or expanding large exponents.
func canonicalFloat(value string) string {
	v := strings.TrimPrefix(strings.ToLower(value), "+")
	if !coreFloat.MatchString(v) {
		return v // Includes the infinities and NaN.
	}
	mantissa, exponent, _ := strings.Cut(v, "e")
	power := new(big.Int)
	if exponent != "" {
		power.SetString(exponent, 10)
	}
	whole, fraction, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0"
	}
	significand := strings.TrimRight(digits, "0")
	power.Add(power, big.NewInt(int64(len(digits)-len(significand)-len(fraction))))
	if strings.HasPrefix(mantissa, "-") {
		significand = "-" + significand
	}
	return significand + "e" + power.String()
}
