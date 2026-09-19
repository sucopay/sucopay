package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/problem"
)

// maxErrorBytes bounds what something else's failure can put in a line.
const maxErrorBytes = 512

// Ready reports whether something the instance depends on can be reached. A
// nil Ready is an instance running without that dependency, which is a state
// to report rather than a failure.
type Ready func(context.Context) error

// Dependencies is everything a route may take from the instance serving it.
//
// Database may be nil, which is what an instance configured without one
// passes. A function rather than an interface, so that "no database" is a
// nil nobody can get wrong: a nil pointer in a non-nil interface would read
// as configured and panic when asked. Credentials, Payments, Webhooks,
// Checkout and Chains are nil in the same instance; [Credentials] says what
// that refuses, [Payments], [Webhooks] and [Checkout] what it answers, and
// [Chains] what it leaves out.
type Dependencies struct {
	Database    Ready
	Credentials Credentials
	Payments    Payments
	Webhooks    Webhooks
	Chains      Chains
	Settling    Settling
	Delivering  Delivering
	Checkout    Checkout
	Refund      Refund
}

// Handler returns the routes an instance serves, each behind what it asks of
// a caller.
func Handler(log *slog.Logger, deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	a := auth{log: log, credentials: deps.Credentials}
	for _, r := range routes(log, deps) {
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
	problem.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyJSON is what a probe is answered with. What a deployment does not have
// is left out rather than written empty, so that a field a reader finds is one
// with something in it.
type readyJSON struct {
	Status      string            `json:"status"`
	Database    string            `json:"database"`
	Credentials string            `json:"credentials,omitempty"`
	Networks    map[string]string `json:"networks,omitempty"`
	Assets      map[string]string `json:"assets,omitempty"`
	Finality    map[string]string `json:"finality,omitempty"`
	Webhooks    string            `json:"webhooks,omitempty"`
}

// ready answers whether the instance can serve, which is what a load balancer
// decides on: the database it keeps payments in, and whether anything is
// reading the chains those payments settle on.
//
// The body also says what credentials are in force, which does not change
// the answer. A deployment holding no credential that writes is one nobody
// can create a payment in, and one that is well by every other measure; and
// were it not ready, there would be no reaching it to make one. Nor does
// the word prove that authentication works: a deployment whose key was
// swapped holds credentials that write and refuses every request.
func ready(log *slog.Logger, database Ready, credentials Credentials, chains Chains,
	decides Settling, delivers Delivering) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if database == nil {
			problem.JSON(w, http.StatusOK, readyJSON{Status: "ok", Database: "none configured"})
			return
		}
		body, err := reached(r.Context(), database, credentials)
		if err != nil {
			// The reason goes to the log, where an operator reads it. What
			// comes back over HTTP names the dependency and nothing else: a
			// readiness probe is read by whoever can reach the port.
			//
			// Quoted, because a database writes this and nothing here vouches
			// for what characters it used.
			logger(r.Context(), log).ErrorContext(r.Context(), "not ready",
				slog.String("dependency", "database"),
				slog.String("error", invisible.Shown(err.Error(), maxErrorBytes)))
			problem.JSON(w, http.StatusServiceUnavailable,
				readyJSON{Status: "unavailable", Database: "unreachable"})
			return
		}
		body.Networks, body.Assets = words(chains)
		// Said and not acted on, for the reason [Settling] gives.
		body.Finality = settling(decides)
		body.Webhooks = delivering(delivers)
		if !serving(body.Networks) {
			// One word for each network and no reason for any of them. A
			// network is named in the document and read through an endpoint
			// that may carry a key, and whoever can reach the port reads this.
			body.Status = "unavailable"
			problem.JSON(w, http.StatusServiceUnavailable, body)
			return
		}
		problem.JSON(w, http.StatusOK, body)
	}
}

// reached asks the database, and then the store over it, for what a probe
// answers when both answer. Each is asked under its own bound, so a probe
// waits at most for the two in turn. A database that answers a ping and not
// a query over its own table is one no request can be authenticated against,
// and it is answered for as one not reached.
//
// credentials is nil where nothing built a store over the database, which
// a test does. There is then nothing to ask.
func reached(ctx context.Context, database Ready, credentials Credentials) (readyJSON, error) {
	if err := database(ctx); err != nil {
		return readyJSON{}, err
	}
	body := readyJSON{Status: "ok", Database: "reachable"}
	if credentials == nil {
		return body, nil
	}
	inForce, err := credentials.InForce(ctx)
	if err != nil {
		return readyJSON{}, err
	}
	body.Credentials = string(inForce)
	return body, nil
}
