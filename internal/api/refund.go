package api

import "net/http"

// Refund serves the page a merchant signs a refund on, reached by a token in
// the path and by no credential. [refund.HTTP] is one, and nil is an instance
// configured without a database, which has no refund to serve a page for.
//
// A method answers the request itself, and returns an error only for a
// dependency it could not reach, with nothing written, as [Checkout] does.
type Refund interface {
	Page(w http.ResponseWriter, r *http.Request) error
	State(w http.ResponseWriter, r *http.Request) error
	Assets(w http.ResponseWriter, r *http.Request) error
}
