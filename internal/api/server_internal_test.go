package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// Read from inside the package rather than through a method exported for the
// purpose. What net/http reports is not something a caller of this package
// asks for, and a way to reach it would exist for the sake of this test.

func TestListen_SendsWhatNetHTTPReportsToTheLog(t *testing.T) {
	t.Parallel()
	var lines bytes.Buffer
	log := slog.New(slog.NewTextHandler(&lines, nil))

	s, err := Listen("127.0.0.1:0", http.NotFoundHandler(), log)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.http.ErrorLog == nil {
		t.Fatal("net/http was left reporting to the standard logger")
	}
	s.http.ErrorLog.Print("something net/http would say")

	if !strings.Contains(lines.String(), "something net/http would say") {
		t.Errorf("what net/http reports went elsewhere:\n%s", lines.String())
	}
}
