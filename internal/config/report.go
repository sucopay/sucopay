package config

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/sucopay/sucopay/internal/invisible"
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

// presence is all a report says of a Hidden: whether there is one.
func presence(h Hidden) string {
	if h == "" {
		return "not set"
	}
	return "set"
}

// Report returns every setting the document was read for, sorted by path. For
// a secret setting the value is replaced by whether it is set, so that a caller
// rendering the report cannot disclose one.
//
// Paths and values are quoted by [invisible.Quote]. Both are chosen by the document, and
// a report is laid out in columns a newline would forge a row of.
//
// Secrecy is decided by path, and a path is built from names the document
// chooses. [Resolve] refuses a network name holding a dot for that reason; a
// Config assembled without it can put a secret at a path this does not match,
// and what hides the value there is its type, [Hidden].
func (r Resolved) Report() []ReportLine {
	values := map[string]string{
		"listen.host":        r.Config.Listen.Host,
		"listen.port":        strconv.Itoa(r.Config.Listen.Port),
		"listen.base_url":    r.Config.Listen.BaseURL,
		"log.level":          r.Config.Log.Level,
		"log.format":         r.Config.Log.Format,
		"database.managed":   strconv.FormatBool(r.Config.Database.Managed),
		"database.url":       presence(r.Config.Database.URL),
		"credentials.key":    presence(r.Config.Credentials.Key),
		"credentials.key_id": r.Config.Credentials.KeyID,
	}
	for name, n := range r.Config.Networks {
		path := "networks." + name
		values[path+".kind"] = n.Kind
		values[path+".poll"] = n.Poll.String()
		values[path+".width"] = strconv.Itoa(n.Width)
		if takes(n.Kind, "chain_id") {
			values[path+".chain_id"] = strconv.FormatUint(n.ChainID, 10)
		}
		if takes(n.Kind, "rpc") {
			// Written whether or not it is set, the way every other secret
			// is: a network reached only through others still has an own to
			// report as not set, and a row missing entirely would read as a
			// setting this kind does not take.
			values[path+".rpc.own"] = presence(n.RPC.Own)
			for i, endpoint := range n.RPC.Others {
				values[fmt.Sprintf("%s.rpc.others[%d]", path, i)] = presence(endpoint)
			}
		}
	}
	for name, a := range r.Config.Assets {
		values["assets."+name+".network"] = string(a.Network())
		values["assets."+name+".reference"] = a.Reference()
		values["assets."+name+".symbol"] = a.Symbol()
		values["assets."+name+".decimals"] = strconv.Itoa(int(a.Decimals()))
	}

	lines := make([]ReportLine, 0, len(values))
	for path, value := range values {
		// The second layer. The values above are text by now, so a secret
		// path that was given anything but its presence is reduced here,
		// whatever it was given.
		if Secret(path) && value != "not set" {
			value = "set"
		}
		lines = append(lines, ReportLine{
			Path:   invisible.Quote(path),
			Value:  invisible.Quote(value),
			Source: r.Sources[path],
			Secret: Secret(path),
		})
	}
	slices.SortFunc(lines, func(a, b ReportLine) int { return cmp.Compare(a.Path, b.Path) })
	return lines
}
