package api

import (
	"log/slog"
	"net/http"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Checkout serves a payer's page, reached by a token in the path and by no
// credential. [checkout.HTTP] is one, and nil is an instance configured
// without a database, which has no payment to serve a page for.
//
// A method answers the request itself, and returns an error only for a
// dependency it could not reach, with nothing written, as [Payments] does.
type Checkout interface {
	Page(w http.ResponseWriter, r *http.Request) error
	State(w http.ResponseWriter, r *http.Request) error
	Attempt(w http.ResponseWriter, r *http.Request) error
	Assets(w http.ResponseWriter, r *http.Request) error
}

// forPayer serves one method of c. The route is open: what admits a payer
// is the token, and the method reads it. c is nil in an instance configured
// without a database, and that is answered as what the instance lacks.
func forPayer(log *slog.Logger, c Checkout,
	method func(Checkout, http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			unavailable(w)
			return
		}
		if err := method(c, w, r); err != nil {
			logger(r.Context(), log).ErrorContext(r.Context(), "could not serve the request",
				slog.String("error", invisible.Shown(err.Error(), maxErrorBytes)))
			unavailable(w)
		}
	}
}
