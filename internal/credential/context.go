package credential

import "context"

type contextKey struct{}

// NewContext returns a context carrying c, for the handler a request reaches
// once it has been authenticated: the request says which account it is from
// through the credential it presented, and through nothing else it carries.
func NewContext(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// FromContext returns the credential a request was authenticated with, and
// whether it was with one. A request on a route that asks for none carries
// none, and a handler that finds none where it expected one is on a route
// declared more open than it is.
func FromContext(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(contextKey{}).(Credential)
	return c, ok
}
