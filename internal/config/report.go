package config

import (
	"cmp"
	"slices"
	"strconv"
)

// ReportLine is one setting as it appears to an operator.
type ReportLine struct {
	Path   string
	Value  string
	Source Source
	Secret bool
}

// Report returns every setting the document was read for, sorted by path.
// A secret setting reports whether it is set instead of its value, so that
// callers cannot leak one by rendering the report.
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
			Path:   path,
			Value:  value,
			Source: r.Sources[path],
			Secret: secret,
		})
	}
	slices.SortFunc(lines, func(a, b ReportLine) int { return cmp.Compare(a.Path, b.Path) })
	return lines
}
