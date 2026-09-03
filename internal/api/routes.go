package api

import (
	"log/slog"
	"net/http"
)

// access is what a route asks of whoever calls it.
type access int

const (
	// unstated is the zero value. A route literal that leaves needs out
	// carries it, and a test refuses it, so that forgetting to say becomes a
	// failure rather than a default in either direction.
	unstated access = iota
	// open answers without a credential.
	open
	// read returns state and changes none of it.
	read
	// write changes state.
	write
)

// route is one path an instance serves, and what a caller must hold to reach it.
type route struct {
	pattern string
	needs   access
	handle  http.HandlerFunc
}

// routes is everything an instance serves.
//
// Not a mux.HandleFunc call per route: a third one added by copying that shape
// carries no stated access, and a [net/http.ServeMux] will not list its
// patterns back for anything to notice afterwards. A test reads this package
// and refuses a registrar anywhere but [Handler].
//
// A handler here closes over database rather than reaching it. routes runs
// while an instance is still starting, and database is nil in a deployment
// configured without one.
func routes(log *slog.Logger, database Ready) []route {
	return []route{
		{pattern: "GET /healthz", needs: open, handle: alive},
		{pattern: "GET /readyz", needs: open, handle: ready(log, database)},
	}
}
