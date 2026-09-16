package api

import (
	"log/slog"
	"net/http"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// Payments serves the payments of one account at a time. [payment.HTTP] is
// one.
//
// A method answers the request itself, and returns an error only for a
// dependency it could not reach, with nothing written. What that is
// answered with, and what is logged of it, is then the same on every route
// and decided in one place, [forAccount].
//
// Nil in an instance configured without a database, which has nothing to
// serve payments over. An interface, as [Credentials] is, and its nil is
// the same one: the interface left unset, never a nil pointer in it, which
// [payment.NewHTTP] does not return.
type Payments interface {
	Create(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Read(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
}

// forAccount serves one method of p for the account the request was
// authenticated as, which [auth.admit] left in the context. P is whichever
// interface serves an account's things, [Payments] or [Webhooks].
//
// method is a method expression, Payments.Create for one, and not a method
// value. A method value is taken from p where the route is built, which is
// while the instance starts, and taking one from a nil interface panics
// there. p is nil in an instance configured without a database. No request
// reaches this there, since admit finds no credential to admit; a route
// declared open would, and is answered with what the instance lacks rather
// than served over nothing.
//
// A request carrying no credential is on a route declared more open than
// the account it serves, and is answered as one presenting none. Nothing
// here serves for an account nobody was authenticated as.
func forAccount[P comparable](log *slog.Logger, p P,
	method func(P, http.ResponseWriter, *http.Request, payment.AccountID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var none P
		if p == none {
			unavailable(w)
			return
		}
		c, ok := credential.FromContext(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		if err := method(p, w, r, payment.AccountID(c.Account)); err != nil {
			// The reason goes to the log, as it does from admit, and the
			// answer names nothing.
			logger(r.Context(), log).ErrorContext(r.Context(), "could not serve the request",
				slog.String("error", invisible.Shown(err.Error(), maxErrorBytes)))
			unavailable(w)
		}
	}
}
