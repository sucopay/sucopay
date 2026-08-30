package config

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// MaxDocumentBytes is the largest configuration document Decode reads. A
// document is written by hand or by a template, so a few hundred kilobytes is
// far above any real one.
const MaxDocumentBytes = 256 << 10

var (
	// ErrDocumentTooLarge reports a document past [MaxDocumentBytes].
	ErrDocumentTooLarge = errors.New("configuration document is too large")
	// ErrDocumentHasAliases reports anchors or aliases in a document.
	ErrDocumentHasAliases = errors.New("configuration document uses anchors or aliases")
)

// Decode turns a configuration document into the shape [Resolve] reads. An
// empty document decodes to an empty mapping.
//
// Errors carry the line and column and nothing of the source. The parser offers
// an excerpt of the surrounding lines, which would put whatever a document
// holds near the mistake into a message that reaches a terminal and a log.
//
// Anchors and aliases are refused. Expanding an alias inside a mapping copies
// what it names, and nothing bounds how often: a document of a few hundred
// bytes can name a copy of a copy until memory runs out. Refusing them also
// keeps a value at a secret path from being reached under another name.
func Decode(b []byte) (map[string]any, error) {
	if len(b) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit is %d", ErrDocumentTooLarge, len(b), MaxDocumentBytes)
	}
	if err := refuseAliases(b); err != nil {
		return nil, err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("configuration document: %s", yaml.FormatError(err, false, false))
	}
	return doc, nil
}

// refuseAliases walks the syntax of a document without building its values, so
// that an alias is found before anything expands it.
func refuseAliases(b []byte) error {
	file, err := parser.ParseBytes(b, 0)
	if err != nil {
		// Leave the report to Decode, which has the message that hides the
		// source.
		return nil
	}
	for _, doc := range file.Docs {
		if doc.Body == nil {
			continue
		}
		found := &aliasFinder{}
		ast.Walk(found, doc.Body)
		if found.at != "" {
			return fmt.Errorf("configuration document: %w: at %s", ErrDocumentHasAliases, found.at)
		}
	}
	return nil
}

type aliasFinder struct{ at string }

func (f *aliasFinder) Visit(node ast.Node) ast.Visitor {
	if f.at != "" {
		return nil
	}
	switch node.(type) {
	case *ast.AnchorNode, *ast.AliasNode:
		if tk := node.GetToken(); tk != nil {
			f.at = fmt.Sprintf("[%d:%d]", tk.Position.Line, tk.Position.Column)
		} else {
			f.at = "an unknown position"
		}
		return nil
	}
	return f
}
