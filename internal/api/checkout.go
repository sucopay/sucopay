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

// forToken serves one method of p. The route is open: what admits whoever
// opened it is the token in the path, and the method reads it. P is whichever
// interface serves a page reached that way, [Checkout] for the payer's or
// [Refund] for the merchant's.
//
// p is nil in an instance configured without a database, and that is answered
// as what the instance lacks.
func forToken[P comparable](log *slog.Logger, c P,
	method func(P, http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var none P
		if c == none {
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
