package config

import (
	"errors"
	"fmt"
	"regexp"

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

// position is the [line:column] the parser reports.
var position = regexp.MustCompile(`\[\d+:\d+\]`)

// where returns the position of a parse failure and nothing else.
//
// Turning the parser's source excerpt off is not enough: it also quotes
// fragments of the document into the message itself, such as `invalid header
// option: "{::"`. The document may be any file SUCO_CONFIG names, and an error
// made from one reaches a terminal and a log. So the parser's words are
// dropped and only the position is passed on; whoever is reading has the file
// open in front of them.
func where(err error) string {
	if at := position.FindString(yaml.FormatError(err, false, false)); at != "" {
		return at
	}
	return "the document"
}

// Decode turns a configuration document into the shape [Resolve] reads. An
// empty document decodes to an empty mapping.
//
// A failure to parse one carries the line and column and nothing else: see
// [where].
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
		return nil, fmt.Errorf("configuration document: %s is not valid YAML", where(err))
	}
	if doc == nil {
		// An empty document unmarshals into nothing at all. Reading a nil map
		// is safe, but a caller holding one has to know that; every document
		// that decodes comes back as a document.
		doc = map[string]any{}
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
