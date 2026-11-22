package tasktemplate

import (
	"encoding/json"
	"fmt"
	"io"
)

// DecodeCatalog reads one JSON array and validates every definition and reference.
func DecodeCatalog(r io.Reader) (Catalog, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode template catalog: %w", err)
	}
	if catalog == nil {
		return nil, fmt.Errorf("template catalog must be a JSON array")
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case io.EOF:
	case nil:
		return nil, fmt.Errorf("unexpected JSON value after template catalog")
	default:
		return nil, fmt.Errorf("read end of template catalog: %w", err)
	}
	seen := make(map[Ref]bool, len(catalog))
	for i, template := range catalog {
		if err := template.checkDefinition(); err != nil {
			return nil, fmt.Errorf("template catalog entry %d: %w", i+1, err)
		}
		if seen[template.Ref] {
			return nil, fmt.Errorf("duplicate template %q revision %q", template.Name, template.Revision)
		}
		seen[template.Ref] = true
	}
	return catalog, nil
}
