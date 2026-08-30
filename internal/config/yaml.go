package config

import (
	"fmt"

	"github.com/goccy/go-yaml"
)

// Decode turns a configuration document into the shape [Resolve] reads. An
// empty document decodes to an empty mapping.
//
// Errors carry the line and an excerpt of the source, so they are returned as
// they arrive rather than reduced to a message.
func Decode(b []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("configuration document: %w", err)
	}
	return doc, nil
}
