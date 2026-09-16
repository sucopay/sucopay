package webhook

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Resolver turns a name into addresses. [net.DefaultResolver] is one; a test
// hands in its own so that what a name resolves to is the test's to say.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Destination is a URL that passed its check, with the addresses the check
// saw. A delivery connects to one of those and not to a fresh resolution:
// between the check and the connection a name can be made to answer with
// another address, which is the one attack a check on its own does not stop.
type Destination struct {
	URL   *url.URL
	Addrs []netip.Addr
}

// inside are the ranges a destination is refused for beyond what
// [netip.Addr] names on its own: the shared address space, where Alibaba
// Cloud keeps its metadata service, the zero network, and broadcast.
var inside = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("255.255.255.255/32"),
}

// Check holds a destination to its shape and to where it leads, and answers
// with what to connect to. Every address the name resolves to is checked,
// and one address inside the deployment refuses the destination whole: a
// name that answers with one address outside and one inside is one whose
// owner chooses which the deployment reaches.
//
// allowed are the addresses the operator let this destination reach although
// they are inside. They are matched against the address and never the name.
//
// What is wrong is answered as problems naming the field the URL came in,
// and the problems carry nothing of the address: an operator reading them
// learns that the destination leads inside, not where.
func Check(ctx context.Context, resolver Resolver, raw string, allowed []netip.Prefix) (Destination, []Problem) {
	refuse := func(message string) (Destination, []Problem) {
		return Destination{}, []Problem{{Field: "url", Message: message}}
	}
	// Before parsing: the URL is stored and shown as it was given, and a
	// character nobody sees in it would be one an operator reading the list
	// does not see either.
	if invisible.Has(raw) {
		return refuse("carries a character a reader cannot see")
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return refuse("not a URL")
	case u.Scheme != "https":
		return refuse("must be https")
	case u.User != nil:
		return refuse("carries a username or password, which a destination may not")
	case u.Hostname() == "":
		return refuse("has no host")
	case strings.ContainsAny(u.Hostname(), "%"):
		// A zone in a literal names an interface of this machine.
		return refuse("names an interface of this machine")
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil || len(addrs) == 0 {
		return refuse("does not resolve")
	}
	checked := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		// IPv4 carried inside IPv6 is the IPv4 address, and is checked as
		// one: ::ffff:169.254.169.254 is the metadata service.
		addr = addr.Unmap()
		if isInside(addr) && !isAllowed(addr, allowed) {
			return refuse("resolves to an address inside the deployment")
		}
		checked = append(checked, addr)
	}
	return Destination{URL: u, Addrs: checked}, nil
}

// isInside says whether an address is one the deployment must not be made to
// reach: loopback, link-local (where cloud metadata services live),
// private, the shared address space, the zero network, unspecified,
// multicast and broadcast.
func isInside(a netip.Addr) bool {
	if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsPrivate() || a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range inside {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func isAllowed(a netip.Addr, allowed []netip.Prefix) bool {
	for _, p := range allowed {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Dial connects to the address a check saw, under the name the URL carries,
// so that the certificate is verified against the name while the connection
// goes where the check looked. The port is the URL's, or 443.
func (d Destination) Dial(ctx context.Context, dialer *net.Dialer) (net.Conn, error) {
	port := d.URL.Port()
	if port == "" {
		port = "443"
	}
	var last error
	for _, addr := range d.Addrs {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}
