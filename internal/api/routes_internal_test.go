package api

import (
	"io"
	"log/slog"
	"slices"
	"testing"
)

// Read from inside the package rather than through a surface exported for the
// purpose. A ServeMux does not list its patterns, so what a route requires
// cannot be asked for from outside, and a way to ask would exist for this test
// and nothing else.

// stated is every access a route may carry. The zero one is not among them, so
// that a literal leaving needs out lands somewhere this test refuses rather
// than on whichever access happened to be written first.
var stated = []access{open, read, write}

func TestRoutes_EveryRouteSaysWhatReachingItRequires(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// What the cases below rest on. A route literal that leaves needs out
	// carries the zero access, so the zero one has to be an access no route
	// may state. Reorder the constants and that stops being true.
	var zero access
	if unstated != zero || slices.Contains(stated, zero) {
		t.Fatal("the zero access is one a route may state, so leaving needs out would pass")
	}

	for _, r := range routes(quiet, nil) {
		t.Run(r.pattern, func(t *testing.T) {
			if !slices.Contains(stated, r.needs) {
				t.Errorf("does not say what reaching it requires")
			}
		})
	}
}
