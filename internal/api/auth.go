package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
)

// Credentials is where the credential a request presents is looked up.
// [credential.Postgres] is one.
//
// An instance configured without a database has nowhere to look and passes
// nil. A route that asks for a credential then refuses every request, as it
// would over an empty table: no credential exists there for one to present.
//
// An interface, where [Ready] is a function so that a nil pointer cannot
// hide in it. What this names is a store with methods, not one call, and
// the pointer that would hide has no source: [credential.NewPostgres] never
// returns nil, and the nil that means no store is the interface itself,
// left unset.
type Credentials interface {
	FindByToken(ctx context.Context, token credential.Token) (credential.Credential, error)
}

// auth is what every route is registered behind.
type auth struct {
	log         *slog.Logger
	credentials Credentials
}

// admit returns next behind what the route asks for. A request reaches next
// when it presents a credential that may do what the route does, with that
// credential in its context, and is answered here otherwise.
//
// This runs after the mux has matched, so a path nothing serves is 404 and
// the wrong method is 405 whether or not a credential came with the request.
// What the instance serves is what its documentation says it serves, and
// answering 401 to those would cost every client's first mistake the reason.
func (a auth) admit(needs access, next http.HandlerFunc) http.HandlerFunc {
	if needs == open {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r.Header.Get("Authorization"))
		if !ok || a.credentials == nil {
			unauthorized(w)
			return
		}
		c, err := a.credentials.FindByToken(r.Context(), token)
		if errors.Is(err, credential.ErrNotFound) {
			unauthorized(w)
			return
		}
		if err != nil {
			// A database the instance could not reach, or one that did not
			// answer in time. The reason goes to the log; over HTTP it would
			// tell whoever is asking what the instance is behind on, and they
			// were not asked for anything yet.
			logger(r.Context(), a.log).ErrorContext(r.Context(), "could not look up the credential",
				slog.String("error", invisible.Shown(err.Error(), maxErrorBytes)))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		// Not needs == write: an access this does not know, the zero one
		// included, is treated as the one that asks the most, so that a
		// route saying nothing admits nothing a read-only credential may do.
		if c.Scope != credential.ScopeAccount || (needs != read && c.Capability != credential.ReadWrite) {
			forbidden(w)
			return
		}
		next(w, r.WithContext(credential.NewContext(r.Context(), c)))
	}
}

// bearer reads the token out of an Authorization header, and reports whether
// the header carried the bearer scheme at all.
//
// Whatever follows the scheme is the token, as it came. A token is looked up
// by the hash of its text, so one that is the wrong length or the wrong
// characters hashes to what no row holds and is not found, which is the
// answer to one nobody issued. Checking its shape here would give a
// malformed one an answer of its own.
//
// The scheme is compared as [http.Request.BasicAuth] compares its own: a
// scheme is case-insensitive, and the token after it is not.
func bearer(header string) (credential.Token, bool) {
	scheme, token, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return credential.Token(token), true
}

// unauthorized answers a request that presented no credential this instance
// holds: none, one under another scheme, one nobody issued, one that was
// revoked, one made under another key, or one that is not the shape of a
// token. One answer, the same to the byte, for all of them. Which of those
// it was is what whoever is guessing at a token would want to know.
//
// The header names the scheme, which RFC 9110 requires of a 401, and names
// no error alongside it. RFC 6750 would have a malformed token answered 400
// and a revoked one told so, and both are answers of their own.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

// forbidden answers a credential this instance holds and will not act on
// here: a read-only one on a route that writes, or one of the deployment on
// a route of an account, which is every route that asks for one. Unlike
// [unauthorized] this says so. Whoever presents a credential holds it, and
// which of the two capabilities it has is what they chose when they made it.
func forbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
}
