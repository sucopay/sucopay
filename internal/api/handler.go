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

// Handler returns the routes an instance serves, each behind what it asks of
// a caller.
//
// database may be nil, which is what an instance configured without one
// passes. A function rather than an interface, so that "no database" is a nil
// nobody can get wrong: a nil pointer in a non-nil interface would read as
// configured and panic when asked. credentials is nil in the same instance,
// and [Credentials] says what that refuses.
func Handler(log *slog.Logger, database Ready, credentials Credentials) http.Handler {
	mux := http.NewServeMux()
	a := auth{log: log, credentials: credentials}
	for _, r := range routes(log, database) {
		mux.HandleFunc(r.pattern, a.admit(r.needs, r.handle))
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
