package webhook

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// carrying are the IPv6 prefixes that carry an IPv4 address inside them and
// are delivered to it: the NAT64 well-known prefix and its local-use
// counterpart, and 6to4. An address under one is checked as the IPv4
// address it carries, as an IPv4-mapped one is; where the four bytes sit
// differs. Under a /96 they are the last four. Under the local-use /48 they
// may be the last four, when an operator uses a /96 within it, or split
// around byte 8, which RFC 6052 keeps clear, when the /48 itself is the
// prefix; both are read, since which the operator chose is not knowable
// from the address. 6to4 puts them right after its two bytes.
var carrying = []struct {
	prefix netip.Prefix
	at     [][4]int
}{
	{netip.MustParsePrefix("64:ff9b::/96"), [][4]int{{12, 13, 14, 15}}},
	{netip.MustParsePrefix("64:ff9b:1::/48"), [][4]int{{12, 13, 14, 15}, {6, 7, 9, 10}}},
	{netip.MustParsePrefix("2002::/16"), [][4]int{{2, 3, 4, 5}}},
}

// MaxURLBytes bounds a destination. Receivers refuse longer request lines
// long before this, and a bound here answers at registration rather than at
// the first delivery.
const MaxURLBytes = 2048

// resolveTimeout bounds one resolution of a name. A resolver that does not
// answer in this time is one a delivery would not reach either.
const resolveTimeout = 5 * time.Second

// unreachable is the one answer for a destination that does not resolve and
// one that resolves inside the deployment. Two answers would let whoever
// holds a credential learn, name by name, which names the deployment's
// resolver knows; a merchant who mistyped a name will look at the name.
const unreachable = "cannot be delivered to from this deployment"

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
	if len(raw) > MaxURLBytes {
		return refuse("longer than " + strconv.Itoa(MaxURLBytes) + " bytes")
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
	case !portOK(u.Port()):
		return refuse("has a port outside 1 to 65535")
	}
	resolving, stop := context.WithTimeout(ctx, resolveTimeout)
	defer stop()
	addrs, err := resolver.LookupNetIP(resolving, "ip", u.Hostname())
	if err != nil || len(addrs) == 0 {
		return refuse(unreachable)
	}
	checked := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		// IPv4 carried inside IPv6 is the IPv4 address, and is checked as
		// one: ::ffff:169.254.169.254 is the metadata service, and so is
		// 64:ff9b::a9fe:a9fe behind NAT64.
		addr = addr.Unmap()
		if isInside(addr) && !isAllowed(addr, allowed) {
			return refuse(unreachable)
		}
		for _, carried := range carriedIPv4(addr) {
			if isInside(carried) && !isAllowed(carried, allowed) {
				return refuse(unreachable)
			}
		}
		checked = append(checked, addr)
	}
	return Destination{URL: u, Addrs: checked}, nil
}

// portOK says whether a port as a URL carries it is one a connection can be
// made to. url.Parse admits any digits; an empty port is 443.
func portOK(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// carriedIPv4 is each IPv4 address an IPv6 address under one of the
// carrying prefixes may be delivered to, and nothing for any other address.
func carriedIPv4(a netip.Addr) []netip.Addr {
	if !a.Is6() {
		return nil
	}
	b := a.As16()
	for _, c := range carrying {
		if !c.prefix.Contains(a) {
			continue
		}
		out := make([]netip.Addr, 0, len(c.at))
		for _, at := range c.at {
			out = append(out, netip.AddrFrom4([4]byte{b[at[0]], b[at[1]], b[at[2]], b[at[3]]}))
		}
		return out
	}
	return nil
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
