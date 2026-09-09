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
// A handler here closes over what it takes from deps, the Database,
// Credentials, Payments and Chains, rather than reaching them. routes runs
// while an instance is still starting, and all four are nil in one configured
// without a database.
func routes(log *slog.Logger, deps Dependencies) []route {
	return []route{
		{pattern: "GET /healthz", needs: open, handle: alive},
		{pattern: "GET /readyz", needs: open, handle: ready(log, deps.Database, deps.Credentials, deps.Chains)},
		{pattern: "POST /payments", needs: write, handle: forAccount(log, deps.Payments, Payments.Create)},
		{pattern: "GET /payments/{id}", needs: read, handle: forAccount(log, deps.Payments, Payments.Read)},
	}
}
