package payment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/problem"
)

// Assets is the list a request names an asset from, by the name the
// configuration document lists it under.
type Assets interface {
	Asset(name string) (Asset, bool)
}

// maxQuoted bounds what a problem repeats of a request: enough to recognise
// the value, and not the whole body.
const maxQuoted = 128

// shown is a value of the body as a problem repeats it: cut to maxQuoted and
// quoted when it holds a character a reader cannot see; written "" when it is
// empty, the way an empty key is.
func shown(value string) string {
	if value == "" {
		return `""`
	}
	return invisible.Shown(value, maxQuoted)
}

// request is what a body asks for, once read. expiresAt is zero and metadata
// nil when the body leaves them out.
type request struct {
	asset     Asset
	amount    Money
	expiresAt time.Time
	metadata  map[string]string
	returnURL string
}

// requestKeys is every key a body may hold.
var requestKeys = []string{"amount", "asset", "expires_at", "metadata", "return_url"}

// MaxReturnURLBytes bounds where a payer is sent back to.
const MaxReturnURLBytes = 2048

// IdempotencyKeyHeader carries the key a merchant sends so that a retry of
// one request opens one payment.
const IdempotencyKeyHeader = "Idempotency-Key"

// MaxIdempotencyKeyBytes bounds that key, as the implementations merchants
// have already met do.
const MaxIdempotencyKeyBytes = 255

// readIdempotencyKey reads the key out of the headers, and reports what is
// wrong with it instead.
//
// The draft that defines the field writes the value as a structured field
// string, in quotes; the clients merchants use send it bare. Both are read,
// and the quotes are syntax rather than part of the key. A key is printable
// ASCII without the two characters that spelling would have to escape, so
// that two keys that look the same are the same key.
//
// No key at all is not a problem: the field is optional, and a request
// without one opens a payment as it always did.
func readIdempotencyKey(h http.Header) (string, Problems) {
	given, ok := h[textproto.CanonicalMIMEHeaderKey(IdempotencyKeyHeader)]
	refuse := func(message string) (string, Problems) {
		return "", Problems{{Field: IdempotencyKeyHeader, Message: message}}
	}
	switch {
	case !ok:
		return "", nil
	case len(given) > 1:
		return refuse("given more than once, and two keys name two requests")
	}
	key := given[0]
	if len(key) >= 2 && key[0] == '"' && key[len(key)-1] == '"' {
		key = key[1 : len(key)-1]
	}
	switch {
	case key == "":
		return refuse("empty")
	case len(key) > MaxIdempotencyKeyBytes:
		return refuse(fmt.Sprintf("longer than %d bytes", MaxIdempotencyKeyBytes))
	}
	for i := range len(key) {
		if c := key[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return refuse("carries a character a key may not: printable ASCII, and no quote or backslash")
		}
	}
	return key, nil
}

// checkReturnURL says what is wrong with where a merchant wants the payer
// sent back to, and nothing when it is a place the checkout page may send
// them. https, since the page sends the payer there; a scheme that runs in
// the page, javascript: for one, would run there as the merchant's script.
// http is allowed for the machine itself, which is where a merchant
// develops.
func checkReturnURL(raw string) Problems {
	if len(raw) > MaxReturnURLBytes {
		return Problems{{Field: "return_url", Message: fmt.Sprintf("longer than %d bytes", MaxReturnURLBytes)}}
	}
	if invisible.Has(raw) {
		return Problems{{Field: "return_url", Message: "carries a character a reader cannot see"}}
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return Problems{{Field: "return_url", Message: "not a URL"}}
	case u.User != nil:
		return Problems{{Field: "return_url", Message: "carries a username or password, which it may not"}}
	case u.Scheme == "https" && u.Hostname() != "":
		return nil
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"):
		return nil
	}
	return Problems{{Field: "return_url", Message: "must be https, or http on localhost"}}
}

// readRequest reads a body into what [Service.Open] takes from one, or
// reports everything that keeps it from being read, so that a client fixes a
// body in one round. A body that is not one JSON object is one problem with
// no field. Otherwise each problem's field is the key it is about, and the
// problems come in one order however the body is written: keys the API does
// not read, then asset, amount, return_url, expires_at and metadata, then a required key
// the body leaves out. Open judges the values of a body once it is read.
//
// Reading the amount in the asset's units takes the asset. A body naming no
// asset the list has still has its amount checked for shape, so that a sign
// or an exponent is reported along with the name, and only the places after
// the point wait for an asset to count them against.
func readRequest(body []byte, assets Assets) (request, Problems) {
	raw, err := decodeObject(body)
	if err != nil {
		return request{}, Problems{{Message: err.Error()}}
	}
	var problems Problems
	fail := func(field, format string, args ...any) {
		problems = append(problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	// str reads key as a string, reporting a value of any other kind. The
	// second result is whether there is a string to go on with.
	str := func(key string) (string, bool) {
		v, ok := raw[key]
		if !ok {
			return "", false
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || string(v) == "null" {
			fail(key, "want a string, got %s", kindOf(v))
			return "", false
		}
		return s, true
	}

	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if slices.Contains(requestKeys, key) {
			continue
		}
		// A problem with no field is one about the body as a whole, and a
		// key of no characters is a key all the same. It is written quoted.
		if key == "" {
			key = `""`
		}
		fail(key, "unknown key")
	}
	var r request
	asset, listed := Asset{}, false
	if name, ok := str("asset"); ok {
		if asset, listed = assets.Asset(name); !listed {
			fail("asset", "%s is not an asset this instance lists", shown(name))
		}
	}
	if s, ok := str("amount"); ok {
		switch {
		case !listed:
			if _, _, err := splitUnits(s); err != nil {
				fail("amount", "%v", err)
			}
		default:
			n, err := parseUnits(s, asset.Decimals())
			if err != nil {
				fail("amount", "%v", err)
				break
			}
			if r.amount, err = NewMoney(asset, n); err != nil {
				fail("amount", "%v", err)
			}
		}
	}
	if s, ok := str("return_url"); ok {
		if found := checkReturnURL(s); found != nil {
			problems = append(problems, found...)
		} else {
			r.returnURL = s
		}
	}
	if s, ok := str("expires_at"); ok {
		t, err := time.Parse(time.RFC3339, s)
		switch {
		case err != nil:
			fail("expires_at", "%s is not an RFC 3339 time", shown(s))
		case t.IsZero():
			// The zero time is how a body that leaves expires_at out is read,
			// and would be given the default deadline in place of its own.
			fail("expires_at", "%s is not after now", shown(s))
		}
		r.expiresAt = t
	}
	if v, ok := raw["metadata"]; ok {
		var what string
		if r.metadata, what = readMetadata(v); what != "" {
			fail("metadata", "%s", what)
		}
	}
	for _, key := range []string{"asset", "amount"} {
		if _, ok := raw[key]; !ok {
			fail(key, "none given")
		}
	}
	if len(problems) > 0 {
		return request{}, problems
	}
	r.asset = asset
	return r, nil
}

// readMetadata reads v as an object of strings, or says what v is instead: a
// value of another kind, or an object with such a value under one of its
// keys. Each value is read on its own: encoding/json reads null into a string
// as an empty one, and a client that wrote null did not write that.
func readMetadata(v json.RawMessage) (map[string]string, string) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(v, &entries); err != nil || entries == nil {
		return nil, fmt.Sprintf("want an object of strings, got %s", kindOf(v))
	}
	metadata := make(map[string]string, len(entries))
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		var s string
		if err := json.Unmarshal(entries[key], &s); err != nil || string(entries[key]) == "null" {
			return nil, fmt.Sprintf("want an object of strings, got %s under %s", kindOf(entries[key]), shown(key))
		}
		metadata[key] = s
	}
	return metadata, ""
}

// kindOf names what a JSON value is, in JSON's words, for a message that
// wanted another kind. v is a value the decoder handed out, so it is not
// empty and starts with the character that tells its kind.
func kindOf(v json.RawMessage) string {
	switch v[0] {
	case '{':
		return "an object"
	case '[':
		return "an array"
	case '"':
		return "a string"
	case 'n':
		return "null"
	case 't', 'f':
		return "a boolean"
	}
	return "a number"
}

// decodeObject reads body as one JSON object with nothing after it. The
// error says what the body is instead, and repeats none of it beyond the
// one character the decoder names.
func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	var kind *json.UnmarshalTypeError
	switch err := dec.Decode(&raw); {
	case errors.Is(err, io.EOF):
		return nil, errors.New("body is empty, want a JSON object")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, errors.New("body ends inside the JSON")
	case errors.As(err, &kind):
		return nil, fmt.Errorf("body is a JSON %s, want an object", kind.Value)
	case err != nil:
		return nil, fmt.Errorf("body is not JSON: %s", shown(err.Error()))
	case raw == nil:
		return nil, errors.New("body is null, want a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("body goes on after the JSON object")
	}
	return raw, nil
}

// asFields is problems as [problem.Refuse] writes them.
func asFields(problems Problems) []problem.Field {
	out := make([]problem.Field, 0, len(problems))
	for _, p := range problems {
		out = append(out, problem.Field{Field: p.Field, Message: p.Message})
	}
	return out
}

// Accepted says whether an account accepts payment in an asset, and where on
// the asset's network a payment of it is paid to.
type Accepted interface {
	Destination(ctx context.Context, account AccountID, asset Asset) (Address, bool, error)
}

// MaxBodyBytes bounds the body of a request. A body carrying as much metadata
// as a payment may hold fits in a quarter of it.
const MaxBodyBytes = 64 << 10

// RefundLinks makes what a refund's signing page is reached by. Declared
// here, where it is called; the refund package is one.
type RefundLinks interface {
	// Token is what the row keeps of the page's token.
	Token(id RefundID) PageToken
	// URL is where the merchant signs the refund.
	URL(id RefundID) string
}

// Links makes what a payment's checkout page is reached by. Declared here,
// where it is called; the checkout package is one.
type Links interface {
	// Checkout is what the row keeps of the page's token.
	Checkout(id ID) Checkout
	// CheckoutURL is where a merchant sends the payer.
	CheckoutURL(id ID) string
}

// HTTP serves payments over HTTP, one account's at a time: every method takes
// the account the caller has authenticated, and answers for that account's
// payments only.
type HTTP struct {
	service  *Service
	assets   Assets
	accepted Accepted
	links    Links
	refunds  RefundLinks
}

// NewHTTP serves the payments of service, naming assets from assets, paying
// them to where accepted says, and giving each a checkout page through
// links.
func NewHTTP(service *Service, assets Assets, accepted Accepted, links Links, refunds RefundLinks) *HTTP {
	return &HTTP{service: service, assets: assets, accepted: accepted, links: links, refunds: refunds}
}

// Create opens a payment for account from the body of r and answers with it,
// or answers with what was wrong with the body. The error is a dependency not
// reached, with nothing written: the caller answers for that, in the one way
// it answers for every dependency.
func (h *HTTP) Create(w http.ResponseWriter, r *http.Request, account AccountID) error {
	key, problems := readIdempotencyKey(r.Header)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem.Refuse(w, http.StatusRequestEntityTooLarge, "too_large", nil)
		return nil
	case err != nil:
		// With whatever was wrong with the key, so that a request wrong in
		// both is answered once. A body over the limit is answered by the
		// limit alone: there is nothing else to say about it.
		problems = append(problems, Problem{Message: "the body ended before the request said it would"})
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(problems))
		return nil
	}
	req, found := readRequest(body, h.assets)
	// Everything wrong with the request in one answer, the field among the
	// keys of the body, so that a merchant fixes it in one round.
	problems = append(problems, found...)
	if problems != nil {
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(problems))
		return nil
	}
	destination, accepted, err := h.accepted.Destination(r.Context(), account, req.asset)
	if err != nil {
		return err
	}
	if !accepted {
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(Problems{{Field: "asset",
			Message: "not accepted by this account: nothing says where a payment of it is paid to"}}))
		return nil
	}
	// Open mints its own id; OpenAs takes one, because the checkout token
	// is derived from the id and the row's part of it is written with the
	// payment.
	id, err := NewID()
	if err != nil {
		return err
	}
	var idempotency Idempotency
	if key != "" {
		// The bytes that were read, rather than the request rebuilt from
		// them: what a retry has to match is what it sent.
		sum := sha256.Sum256(body)
		idempotency = Idempotency{Key: key, BodyHash: sum[:]}
	}
	p, replayed, err := h.service.OpenAs(r.Context(), account, id, Request{
		Amount:      req.amount,
		Destination: destination,
		Metadata:    req.metadata,
		ExpiresAt:   req.expiresAt,
		ReturnURL:   req.returnURL,
		Checkout:    h.links.Checkout(id),
		Idempotency: idempotency,
	})
	var refused Problems
	switch {
	case errors.Is(err, ErrIdempotencyKeyUsed):
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Field:   IdempotencyKeyHeader,
			Message: "already used for another body. Send that body, or a key nothing has used"}})
		return nil
	case errors.As(err, &refused):
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(refused))
		return nil
	case err != nil:
		return err
	}
	if replayed {
		// The answer is the payment the key opened, read now rather than a
		// copy of what the first answer said, so a merchant is told which
		// it is.
		w.Header().Set("Idempotent-Replayed", "true")
	}
	// A replayed answer is a read of a payment that may have been paid since,
	// and it says what a read of it says. A payment opened by this request is
	// not asked about: nothing arrives for a payment that did not exist a
	// moment ago.
	var transfer *Transfer
	if replayed {
		if transfer, err = h.seen(r.Context(), account, p.ID()); err != nil {
			return err
		}
	}
	w.Header().Set("Location", "/payments/"+p.ID().String())
	problem.JSON(w, http.StatusCreated, h.body(p, transfer))
	return nil
}

// body is a payment as a route answers with it: what an event carries, and
// the checkout URL, which an event does not. The URL carries the page's
// token, which is a key to the payment's outcome, and a receiver's log is
// not where one belongs.
func (h *HTTP) body(p *Payment, t *Transfer) paymentJSON {
	body := bodyJSON(p)
	body.Transfer = t.shown()
	if p.Checkout().IsSet() {
		u := h.links.CheckoutURL(p.ID())
		body.CheckoutURL = &u
	}
	return body
}

// seen is the transfer a read of the payment answers with, and nil where none
// has been seen for it.
func (h *HTTP) seen(ctx context.Context, account AccountID, id ID) (*Transfer, error) {
	t, found, err := h.service.MatchedTransfer(ctx, account, id)
	if err != nil || !found {
		return nil, err
	}
	return &t, nil
}

// Read answers with the payment of account the path's id names, or that
// there is none. An identifier of another account's payment, of no payment,
// and of no shape are answered alike, so that the answer says nothing about
// what exists. The error is as for [HTTP.Create].
func (h *HTTP) Read(w http.ResponseWriter, r *http.Request, account AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	p, err := h.service.Find(r.Context(), account, id)
	switch {
	case errors.Is(err, ErrNotFound):
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	case err != nil:
		return err
	}
	transfer, err := h.seen(r.Context(), account, id)
	if err != nil {
		return err
	}
	body := h.body(p, transfer)
	held, err := h.service.Refunded(r.Context(), account, p)
	if err != nil {
		return err
	}
	body.Refunded = held
	problem.JSON(w, http.StatusOK, body)
	return nil
}

// paymentJSON is a payment as a response writes it. The asset is written by
// what identifies and describes it, without the name the document lists it
// under: a payment keeps its network and reference when the document comes
// to list the asset under another name, or not at all.
type paymentJSON struct {
	ID          ID                `json:"id"`
	Status      Status            `json:"status"`
	Asset       assetJSON         `json:"asset"`
	Amount      string            `json:"amount"`
	Received    *string           `json:"received"`
	Destination Address           `json:"destination"`
	Metadata    map[string]string `json:"metadata"`
	ExpiresAt   time.Time         `json:"expires_at"`
	CreatedAt   time.Time         `json:"created_at"`
	ReturnURL   *string           `json:"return_url"`
	CheckoutURL *string           `json:"checkout_url,omitempty"`
	// Transfer is the transfer seen for the payment, and null until one
	// has been. It is what a merchant checks against a node of their own.
	Transfer *transferJSON `json:"transfer"`
	// Refunded is what the payment's refunds hold of what arrived, in the
	// asset's units. What can still be refunded is received less this.
	Refunded string `json:"refunded"`
}

// assetJSON is an asset as a response writes it.
type assetJSON struct {
	Network   Network `json:"network"`
	Reference string  `json:"reference"`
	Symbol    string  `json:"symbol"`
	Decimals  uint8   `json:"decimals"`
}

// bodyJSON renders p as a response writes it. Amounts are in the asset's
// units, as a request writes them; received is null until something arrives;
// metadata a request left out is an empty object, so that a client reads one
// shape.
func bodyJSON(p *Payment) paymentJSON {
	asset := p.Asset()
	var received *string
	if p.Received().IsSet() {
		units := p.Received().Units()
		received = &units
	}
	metadata := p.Metadata()
	if metadata == nil {
		metadata = map[string]string{}
	}
	var returnURL *string
	if u := p.ReturnURL(); u != "" {
		returnURL = &u
	}
	return paymentJSON{
		ID:     p.ID(),
		Status: p.Status(),
		Asset: assetJSON{
			Network:   asset.Network(),
			Reference: asset.Reference(),
			Symbol:    asset.Symbol(),
			Decimals:  asset.Decimals(),
		},
		Amount:      p.Amount().Units(),
		Received:    received,
		Destination: p.Destination(),
		Metadata:    metadata,
		ExpiresAt:   p.ExpiresAt(),
		CreatedAt:   p.CreatedAt(),
		ReturnURL:   returnURL,
		// Nothing has been refunded of a payment an event is about: only a
		// succeeded payment can be refunded, and nothing follows succeeded,
		// so no event is written after a refund could exist. A read is where
		// the count is taken.
		Refunded: "0",
	}
}

// transferJSON is a transfer as a payment carries it: what a merchant can
// check against a node of their own, and nothing that says it is final.
//
// The network and where the money went are not repeated. The payment already
// answers both, and a transfer that matched went to the payment's
// destination or it would not have matched.
//
// Value is in the asset's smallest unit, which is what the chain moved and
// not what amount and received are written in.
type transferJSON struct {
	Tx          string    `json:"tx"`
	BlockHeight uint64    `json:"block_height"`
	BlockHash   string    `json:"block_hash"`
	BlockTime   time.Time `json:"block_time"`
	From        string    `json:"from"`
	Value       string    `json:"value"`
}

// shown is a transfer as a body carries it, and nil for a payment nothing has
// been seen for.
func (t *Transfer) shown() *transferJSON {
	if t == nil {
		return nil
	}
	return &transferJSON{
		Tx: t.Tx, BlockHeight: t.BlockHeight, BlockHash: t.BlockHash,
		BlockTime: t.BlockTime.UTC(), From: t.From, Value: t.Value,
	}
}

// AnnounceConfirming is the event a payment produces when a transfer for it
// is seen, ahead of finality: the payment as a read of it would answer,
// with the transfer beside it.
func AnnounceConfirming(p *Payment, t Transfer) (Event, error) {
	payload, err := told(p, &t)
	if err != nil {
		return Event{}, err
	}
	return Event{Name: "attempt.confirming", Payload: payload}, nil
}

// told is a payment as an event carries it, with the transfer seen for it or
// nil. One shape for every event, so that what a merchant is told and what a
// merchant can read stay the same thing.
//
// The payload is written without escaping the characters that have a meaning
// in a page, because it is not one: it goes to a merchant's endpoint. Escaping
// them spends six bytes where the value had one, and a payment carrying
// metadata at every bound this package allows would come to more than an event
// is allowed to be.
func told(p *Payment, t *Transfer) ([]byte, error) {
	body := bodyJSON(p)
	body.Transfer = t.shown()
	var payload bytes.Buffer
	writer := json.NewEncoder(&payload)
	writer.SetEscapeHTML(false)
	if err := writer.Encode(body); err != nil {
		return nil, fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	return bytes.TrimRight(payload.Bytes(), "\n"), nil
}

// Announce is the event a payment produces on reaching the status it is in,
// carrying the payment as a read of it would answer. What a merchant is told
// and what a merchant can ask for are then the same thing.
//
// The transfer is the one seen for the payment, and nil where none has been:
// a caller that moves a payment reads it, as a read of the payment does.
func Announce(p *Payment, t *Transfer) (Event, error) {
	payload, err := told(p, t)
	if err != nil {
		return Event{}, err
	}
	return Event{Name: "payment." + p.Status().String(), Payload: payload}, nil
}
