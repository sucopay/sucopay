package config

import (
	"cmp"
	"slices"
	"strconv"
)

// ReportLine is one setting as it appears to an operator.
type ReportLine struct {
	// Path is the dotted path of the setting, such as "listen.port".
	Path string
	// Value is the resolved value, or whether it is set when Secret is true.
	Value string
	// Source is where the value came from.
	Source Source
	// Secret says whether Value was reduced to whether the setting is set.
	Secret bool
}

// Report returns every setting the document was read for, sorted by path. For
// a secret setting the value is replaced by whether it is set, so that a caller
// rendering the report cannot disclose one.
//
// Paths and values are quoted by [Quote]. Both are chosen by the document, and
// a report is laid out in columns a newline would forge a row of.
//
// Secrecy is decided by path, and a path is built from names the document
// chooses. [Resolve] refuses a network name holding a dot for that reason; a
// Config assembled without it can put a secret at a path this does not match.
func (r Resolved) Report() []ReportLine {
	values := map[string]string{
		"listen.host":      r.Config.Listen.Host,
		"listen.port":      strconv.Itoa(r.Config.Listen.Port),
		"listen.base_url":  r.Config.Listen.BaseURL,
		"database.managed": strconv.FormatBool(r.Config.Database.Managed),
		"database.url":     r.Config.Database.URL,
	}
	for name, n := range r.Config.Networks {
		values["networks."+name+".kind"] = n.Kind
		values["networks."+name+".rpc"] = n.RPC
	}

	lines := make([]ReportLine, 0, len(values))
	for path, value := range values {
		secret := Secret(path)
		if secret {
			value = "not set"
			if values[path] != "" {
				value = "set"
			}
		}
		lines = append(lines, ReportLine{
			Path:   Quote(path),
			Value:  Quote(value),
			Source: r.Sources[path],
			Secret: secret,
		})
	}
	slices.SortFunc(lines, func(a, b ReportLine) int { return cmp.Compare(a.Path, b.Path) })
	return lines
}
