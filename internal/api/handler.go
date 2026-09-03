package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/sucopay/sucopay/internal/invisible"
)

// maxErrorBytes bounds what something else's failure can put in a line.
const maxErrorBytes = 512

// Ready reports whether something the instance depends on can be reached. A
// nil Ready is an instance running without that dependency, which is a state
// to report rather than a failure.
type Ready func(context.Context) error

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

// Handler returns the routes an instance serves.
//
// database may be nil, which is what an instance configured without one
// passes. A function rather than an interface, so that "no database" is a nil
// nobody can get wrong: a nil pointer in a non-nil interface would read as
// configured and panic when asked.
func Handler(log *slog.Logger, database Ready) http.Handler {
	mux := http.NewServeMux()
	for _, r := range routes(log, database) {
		mux.HandleFunc(r.pattern, r.handle)
	}
	return record(log, mux)
}

// alive answers as long as the process is running.
//
// Nothing is consulted on purpose. A liveness probe that failed because a
// database was unreachable would have an orchestrator restart an instance that
// is working, which does not bring the database back. Whether this instance
// can do its job is [ready].
func alive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready answers whether the instance can serve, which is what a load balancer
// decides on.
func ready(log *slog.Logger, database Ready) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if database == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "none configured"})
			return
		}
		if err := database(r.Context()); err != nil {
			// The reason goes to the log, where an operator reads it. What
			// comes back over HTTP names the dependency and nothing else: a
			// readiness probe is read by whoever can reach the port.
			//
			// Quoted, because a database writes this and nothing here vouches
			// for what characters it used.
			logger(r.Context(), log).ErrorContext(r.Context(), "not ready",
				slog.String("dependency", "database"),
				slog.String("error", invisible.Shown(err.Error(), maxErrorBytes)))
			writeJSON(w, http.StatusServiceUnavailable,
				map[string]string{"status": "unavailable", "database": "unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "reachable"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already written, so a failed encode cannot become an
	// error response.
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // nothing to report it to
}
