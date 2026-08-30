package config_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

func lineAt(t *testing.T, lines []config.ReportLine, path string) config.ReportLine {
	t.Helper()
	for _, l := range lines {
		if l.Path == path {
			return l
		}
	}
	t.Fatalf("want a line for %q, got %v", path, lines)
	return config.ReportLine{}
}

func TestReport_SecretValueNeverAppears(t *testing.T) {
	const dsn = "postgres://user:hunter2@db.internal/suco"
	doc := map[string]any{"database": map[string]any{
		"managed": false,
		"url":     "${SUCO_DATABASE_URL}",
	}}
	env := envOf(map[string]string{"SUCO_DATABASE_URL": dsn})

	lines := mustResolve(t, doc, env).Report()

	line := lineAt(t, lines, "database.url")
	if !line.Secret {
		t.Error("database.url is not marked secret")
	}
	if line.Value != "set" {
		t.Errorf("value = %q, want %q", line.Value, "set")
	}
	for _, l := range lines {
		if strings.Contains(l.Value, "hunter2") {
			t.Fatalf("the secret appears in the report at %s", l.Path)
		}
	}
	if line.Source.Var != "SUCO_DATABASE_URL" {
		t.Errorf("source var = %q, want the variable name to stay visible", line.Source.Var)
	}
}

func TestReport_SaysWhenASecretIsAbsent(t *testing.T) {
	lines := mustResolve(t, map[string]any{}, noEnv).Report()

	if got := lineAt(t, lines, "database.url").Value; got != "not set" {
		t.Errorf("value = %q, want %q", got, "not set")
	}
}

func TestReport_ShowsTheValueAndSourceForEverythingElse(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"port": "${SUCO_LISTEN_PORT}"}}
	env := envOf(map[string]string{"SUCO_LISTEN_PORT": "9000"})

	lines := mustResolve(t, doc, env).Report()

	port := lineAt(t, lines, "listen.port")
	if port.Secret {
		t.Error("listen.port is marked secret")
	}
	if port.Value != "9000" {
		t.Errorf("value = %q, want %q", port.Value, "9000")
	}
	if port.Source.Origin != config.FromEnv || port.Source.Var != "SUCO_LISTEN_PORT" {
		t.Errorf("source = %+v, want env SUCO_LISTEN_PORT", port.Source)
	}

	base := lineAt(t, lines, "listen.base_url")
	if base.Source.Origin != config.FromDefault {
		t.Errorf("base_url origin = %v, want default", base.Source.Origin)
	}
}

func TestReport_IsSortedByPath(t *testing.T) {
	lines := mustResolve(t, map[string]any{}, noEnv).Report()

	for i := 1; i < len(lines); i++ {
		if lines[i-1].Path > lines[i].Path {
			t.Fatalf("line %d (%s) sorts after %s", i, lines[i].Path, lines[i-1].Path)
		}
	}
}
