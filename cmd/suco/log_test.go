package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

func TestNewLogger_WritesTheFormatTheDocumentAsked(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ format, wants string }{
		{"json", `"msg":"hello"`},
		{"text", "msg=hello"},
	} {
		t.Run(c.format, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer

			log, err := newLogger(config.Log{Level: "info", Format: c.format}, &out)
			if err != nil {
				t.Fatal(err)
			}
			log.Info("hello")

			if !strings.Contains(out.String(), c.wants) {
				t.Errorf("wrote %q, want %s", out.String(), c.wants)
			}
		})
	}
}

func TestNewLogger_KeepsQuietBelowTheLevelTheDocumentAsked(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer

	log, err := newLogger(config.Log{Level: "warn", Format: "text"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("routine")
	log.Warn("worth reading")

	if strings.Contains(out.String(), "routine") {
		t.Errorf("wrote a line below the level it was given:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "worth reading") {
		t.Errorf("dropped a line at the level it was given:\n%s", out.String())
	}
}

func TestNewLogger_RefusesWhatIsNotALevelOrAFormat(t *testing.T) {
	t.Parallel()
	// The resolver refuses these first, so reaching here means something
	// assembled a Config without it. Failing beats writing nowhere.
	for _, c := range []struct {
		name string
		log  config.Log
	}{
		{"a level nothing defines", config.Log{Level: "loud", Format: "text"}},
		{"a format nothing defines", config.Log{Level: "info", Format: "yaml"}},
		{"nothing at all", config.Log{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := newLogger(c.log, &bytes.Buffer{}); err == nil {
				t.Error("want an error, got none")
			}
		})
	}
}
