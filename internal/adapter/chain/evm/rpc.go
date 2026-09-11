// Package evm reads a chain that speaks Ethereum's JSON-RPC.
//
// What a provider answers is not trusted: every value is checked into a type
// that only holds what the chain writes, the body has a bound, and one call
// has a deadline. Nothing that goes wrong carries the endpoint out with it,
// because the endpoint may carry a key.
package evm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/invisible"
)

const (
	// callTimeout is how long one call is given. It is half the term of the
	// lease a reader holds, so that a call cannot outlive the right to be
	// making it.
	callTimeout = 15 * time.Second
	// maxBody is the most a body may hold once it is decompressed. A span of
	// a thousand blocks with ten logs each is some six megabytes of JSON.
	maxBody = 8 << 20
	// maxItems is the most a provider may answer an array with. Providers
	// stop at ten thousand logs themselves, and one that does not is asking
	// for a narrower span.
	maxItems = 10000
	// maxMessage is how much of a provider's own words are kept.
	maxMessage = 256
	// maxWait is the longest a provider may ask to be left alone for. It is
	// what a duration holds: more than that multiplies out into a negative
	// wait, which is a call made at once.
	maxWait = math.MaxInt64 / int64(time.Second)
	// userAgent names this process to whoever is asked.
	userAgent = "suco"
	// hidden is what the endpoint's own parts read as, wherever they turn up.
	hidden = "[rpc]"
)

// client speaks JSON-RPC to one endpoint.
//
// The deadline and the bound on a body are fields rather than constants so
// that a test can make them small; nothing else writes them.
type client struct {
	endpoint string
	http     *http.Client
	timeout  time.Duration
	maxBody  int64
	hide     *strings.Replacer
}

// newClient returns a client on an endpoint, and refuses one whose answers
// somebody on the way could write.
//
// https anywhere, and http only to this machine: a key on the URL travels in
// the clear over http, and so does the receipt that comes back. The refusal
// does not repeat the endpoint.
func newClient(endpoint string) (*client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("evm: the endpoint is not a URL")
	}
	switch {
	case u.Scheme == "https", u.Scheme == "http" && onThisMachine(u.Hostname()):
	default:
		return nil, errors.New("evm: want an https endpoint, or http to localhost or a loopback address")
	}
	// Hostname rather than Host: https://:8545/ has a Host of ":8545" and
	// names no machine, which the configuration refuses for the same reason.
	if u.Hostname() == "" {
		return nil, errors.New("evm: the endpoint names no host")
	}
	return &client{
		endpoint: endpoint,
		http: &http.Client{
			// A provider that answers with somewhere else to go is answering
			// something this did not ask. Following one would send the call to
			// a host the operator never named, which on this machine is
			// whatever else is listening.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("the provider answered with a redirect")
			},
		},
		timeout: callTimeout,
		maxBody: maxBody,
		hide:    hiding(u),
	}, nil
}

// call asks the provider for one method and reads the answer into result.
func (c *client) call(ctx context.Context, method string, params []any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	asked, err := json.Marshal(struct {
		Version string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{Version: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return c.failed(method, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(asked))
	if err != nil {
		return c.failed(method, err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("user-agent", userAgent)
	// Asked for by name, so that the transport hands the body over compressed
	// and the bound below is read against what it swells to.
	request.Header.Set("accept-encoding", "gzip")

	answer, err := c.http.Do(request)
	if err != nil {
		return c.failed(method, err)
	}
	defer answer.Body.Close() //nolint:errcheck // a body nobody is reading any more

	body, err := c.read(answer)
	if err != nil {
		return c.failed(method, err)
	}
	return c.answered(method, answer, body, result)
}

// read is what the provider sent, decompressed, and no more than the bound.
func (c *client) read(answer *http.Response) ([]byte, error) {
	reader := io.Reader(io.LimitReader(answer.Body, c.maxBody+1))
	if strings.EqualFold(answer.Header.Get("content-encoding"), "gzip") {
		zip, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer zip.Close() //nolint:errcheck // a reader nobody is reading any more
		reader = io.LimitReader(zip, c.maxBody+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > c.maxBody {
		return nil, fmt.Errorf("the answer is longer than %d bytes", c.maxBody)
	}
	return body, nil
}

// answered reads what came back: the provider's own error where there is one,
// and otherwise the result.
func (c *client) answered(method string, answer *http.Response, body []byte, result any) error {
	var envelope struct {
		Error  *rpcError       `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	// A body that is not JSON is only a failure when the status did not
	// already say what went wrong.
	decoded := json.Unmarshal(body, &envelope)

	if status := answer.StatusCode; status != http.StatusOK || envelope.Error != nil {
		return c.refused(method, status, answer.Header.Get("retry-after"), envelope.Error)
	}
	if decoded != nil {
		return c.failed(method, decoded)
	}
	if err := c.counted(envelope.Result); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return c.failed(method, err)
	}
	return nil
}

// counted refuses an array holding more than a provider should ever answer
// with, before anything reads it into values.
func (c *client) counted(result json.RawMessage) error {
	if head := bytes.TrimLeft(result, " \t\r\n"); len(head) == 0 || head[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(result, &items); err != nil {
		return c.hidden(err)
	}
	if len(items) > maxItems {
		return fmt.Errorf("the answer holds %d of them: %w", len(items), chain.ErrTooWide)
	}
	return nil
}

// refused turns a status and a provider's error into what a caller reads: a
// span to narrow, a rate to slow to, or a call that failed.
func (c *client) refused(method string, status int, retryAfter string, raised *rpcError) error {
	said := "no reason"
	if raised != nil {
		said = raised.String(c.say)
	}
	switch {
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%s: %s: %w", method, said, chain.RateLimited{RetryAfter: seconds(retryAfter)})
	case status == http.StatusBadRequest, raised.tooWide():
		return fmt.Errorf("%s: %s: %w", method, said, chain.ErrTooWide)
	case status != http.StatusOK:
		return fmt.Errorf("%s: the provider answered %d: %s", method, status, said)
	default:
		return fmt.Errorf("%s: %s", method, said)
	}
}

// failed reports what went wrong on the way, with nothing of the endpoint in
// it. A transport error carries the URL it was reaching, key and all.
func (c *client) failed(method string, err error) error {
	return fmt.Errorf("%s: %w", method, c.hidden(err))
}

// hidden is the error with the endpoint taken out of it. The transport writes
// the URL it was reaching into its own error, key and all.
func (c *client) hidden(err error) error {
	var reaching *url.Error
	if errors.As(err, &reaching) {
		err = reaching.Err
	}
	return errors.New(c.say(err.Error()))
}

// say is how anything somebody else wrote is repeated: the endpoint taken out,
// and then cut short.
//
// Taken out first: what is replaced has to match whole, and words padded so
// that the endpoint straddles the cut would otherwise leave the first half of
// it in what comes back.
func (c *client) say(s string) string {
	return invisible.Shown(c.hide.Replace(s), maxMessage)
}

// rpcError is what a provider says went wrong.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// String is the error as it may be shown: the code, and the provider's own
// words as [client.say] repeats them. A provider writes the request back into
// its message, and the request holds the key.
func (e *rpcError) String(say func(string) string) string {
	if e == nil {
		return "no reason"
	}
	return fmt.Sprintf("%d %s", e.Code, say(e.Message))
}

// tooWide reports whether the provider refused the span of blocks asked for.
// The codes are what providers answer with; the words differ between them and
// the codes do not.
func (e *rpcError) tooWide() bool {
	return e != nil && (e.Code == -32701 || e.Code == -32602)
}

// seconds reads a Retry-After of whole seconds. A date is how the header may
// also be written, and reading one would be trusting the provider's clock
// against this one.
//
// A number past [maxWait] reads as no wait at all. How long a wait is worth
// taking is the reader's to decide, not the provider's, and it decides on the
// value this gives it.
func seconds(header string) time.Duration {
	wait, err := strconv.Atoi(header)
	if err != nil || wait < 0 || int64(wait) > maxWait {
		return 0
	}
	return time.Duration(wait) * time.Second
}

// hiding is what replaces the endpoint's own parts wherever they turn up: the
// whole URL, the host, and every element of the path and the query. A
// provider that writes the request back into a message writes the key with it.
func hiding(u *url.URL) *strings.Replacer {
	parts := []string{u.String(), u.Host, u.Hostname()}
	if u.User != nil {
		password, _ := u.User.Password()
		parts = append(parts, u.User.String(), u.User.Username(), password)
	}
	parts = append(parts, strings.Split(u.EscapedPath(), "/")...)
	for key, values := range u.Query() {
		parts = append(parts, key)
		parts = append(parts, values...)
	}
	var pairs []string
	for _, part := range parts {
		// A part of one character is a slash away from matching anything.
		if len(part) > 1 {
			pairs = append(pairs, part, hidden)
		}
	}
	return strings.NewReplacer(pairs...)
}

// onThisMachine is the check the configuration makes on an endpoint's host,
// repeated rather than imported: this package reaches nothing of the process
// around it.
func onThisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
