package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
)

// The bounds a send keeps. A receiver has sendTimeout to answer, counting
// the connection; what it answers with is read to responseBytes and kept to
// responseShown, and its headers to headerBytes.
const (
	sendTimeout   = 20 * time.Second
	responseBytes = 4 << 10
	responseShown = 256
	headerBytes   = 64 << 10
)

// The words an attempt is left with when there was no status to record:
// the destination did not pass its check, the receiver did not answer in
// time, the connection was not made or was lost, or the secret to sign
// under is sealed under a key this deployment no longer holds. None of
// them carries an address or what the receiver said.
const (
	ReasonDestination = "destination"
	ReasonTimeout     = "timeout"
	ReasonConnection  = "connection"
	ReasonSecret      = "secret"
)

// userAgent is what a receiver sees the sender as.
const userAgent = "suco"

// Outcome is what one attempt came to.
type Outcome struct {
	// Status is what the receiver answered, and 0 when there was no
	// answer, in which case Reason says why.
	Status int
	Reason string
	// Response is the beginning of what the receiver answered with, as a
	// log may show it: cut short, and without characters a reader cannot
	// see.
	Response string
	Took     time.Duration
}

// Delivered says whether the receiver took the delivery: any 2xx.
func (o Outcome) Delivered() bool { return o.Status >= 200 && o.Status < 300 }

// Sender makes one attempt at a delivery.
//
// The connection goes to the address the check saw, under the name the URL
// carries: the client's own resolution is not used, so that a name which
// answers one address to the check and another to the client is refused
// rather than reached. TLS is 1.2 or later and the certificate is always
// verified. Redirects are not followed; a 3xx is an answer, and not a 2xx.
// The response is not decompressed, and no Accept-Encoding is sent, so that
// a receiver cannot answer with a small body that unpacks to a large one.
type Sender struct {
	resolver Resolver
	client   *http.Client
	now      func() time.Time
	// timeout is sendTimeout, and its own field so that a test can wait
	// less for a receiver that never answers.
	timeout time.Duration
}

// destinationKey carries the checked destination to the dialer through the
// request's context, which is the one thing a Transport hands its dialer.
type destinationKey struct{}

// NewSender opens a sender that resolves through resolver and trusts roots,
// or the system's when roots is nil.
func NewSender(resolver Resolver, roots *x509.CertPool, now func() time.Time) *Sender {
	dialer := &net.Dialer{Timeout: sendTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d, ok := ctx.Value(destinationKey{}).(Destination)
			if !ok {
				return nil, errors.New("webhook: no checked destination to dial")
			}
			return d.Dial(ctx, dialer)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DisableCompression:     true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    sendTimeout,
		MaxResponseHeaderBytes: headerBytes,
		// No Proxy: one from the environment would carry a delivery past the
		// check to wherever the proxy reaches.
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Sender{resolver: resolver, client: client, now: now, timeout: sendTimeout}
}

// Send makes one attempt at d to e, signed under secrets. It answers with
// what the attempt came to and never fails: what did not arrive is an
// outcome the delivery records, not an error the round stops on.
func (s *Sender) Send(ctx context.Context, e Endpoint, allowed []netip.Prefix, secrets []Secret, d Delivery) Outcome {
	started := s.now()
	took := func() time.Duration { return s.now().Sub(started) }

	ctx, stop := context.WithTimeout(ctx, s.timeout)
	defer stop()
	dest, refused := Check(ctx, s.resolver, e.URL, allowed)
	if refused != nil {
		return Outcome{Reason: ReasonDestination, Took: took()}
	}
	timestamp := Timestamp(started)
	signature, err := Sign(secrets, d.ID, timestamp, d.Body)
	if err != nil {
		// A secret of no shape is a row this deployment did not write, and
		// the delivery is not sent unsigned.
		return Outcome{Reason: ReasonSecret, Took: took()}
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, destinationKey{}, dest),
		http.MethodPost, dest.URL.String(), bytes.NewReader(d.Body))
	if err != nil {
		return Outcome{Reason: ReasonDestination, Took: took()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set(HeaderID, string(d.ID))
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, signature)

	resp, err := s.client.Do(req)
	if err != nil {
		reason := ReasonConnection
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason = ReasonTimeout
		}
		return Outcome{Reason: reason, Took: took()}
	}
	defer resp.Body.Close() //nolint:errcheck // the response was read or given up on either way
	// Read to the bound and no further. A body that breaks off, or does not
	// end before the deadline, leaves what was read of it: the status
	// arrived, and stands.
	var body bytes.Buffer
	_, _ = io.Copy(&body, io.LimitReader(resp.Body, responseBytes)) //nolint:errcheck // the status stands, and what was read is kept
	return Outcome{Status: resp.StatusCode, Response: invisible.Shown(body.String(), responseShown), Took: took()}
}
