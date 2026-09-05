package config

import (
	"fmt"
	"os"
)

// PathVar is the environment variable that says where the configuration
// document is. It is the only variable suco Pay reads without the document
// asking for it.
const PathVar = "SUCO_CONFIG"

// Load reads a configuration document and resolves it against env. It reads at
// most [MaxDocumentBytes], and only from a regular file: a named pipe or a
// device would otherwise be read until it ran out of memory.
func Load(path string, env Lookup) (Resolved, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Resolved{}, fmt.Errorf("configuration document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Resolved{}, fmt.Errorf("configuration document: %s is not a regular file", path)
	}
	if info.Size() > MaxDocumentBytes {
		return Resolved{}, fmt.Errorf("configuration document: %w: %d bytes, limit is %d",
			ErrDocumentTooLarge, info.Size(), MaxDocumentBytes)
	}
	// #nosec G304 -- reading the document the operator named is what Load is for
	b, err := os.ReadFile(path)
	if err != nil {
		return Resolved{}, fmt.Errorf("configuration document: %w", err)
	}
	doc, err := Decode(b)
	if err != nil {
		return Resolved{}, err
	}
	return Resolve(doc, env)
}

// Path returns where to read the configuration document from, preferring the
// value of [PathVar] over def.
func Path(env Lookup, def string) string {
	if v, ok := env(PathVar); ok && v != "" {
		return v
	}
	return def
}
