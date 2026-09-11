package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
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
}

// requestKeys is every key a body may hold.
var requestKeys = []string{"amount", "asset", "expires_at", "metadata"}

// readRequest reads a body into what [Service.Open] takes from one, or
// reports everything that keeps it from being read, so that a client fixes a
// body in one round. A body that is not one JSON object is one problem with
// no field. Otherwise each problem's field is the key it is about, and the
// problems come in one order however the body is written: keys the API does
// not read, then asset, amount, expires_at and metadata, then a required key
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

// problemJSON is one problem as a response writes it. A problem that is not
// about one key has no field, and the field is left out rather than written
// empty, as [Problem.String] leaves out the key.
type problemJSON struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// errorJSON is the body of every failed response: one word for what went
// wrong, and for a body that was refused, what was wrong with it.
type errorJSON struct {
	Error    string        `json:"error"`
	Problems []problemJSON `json:"problems,omitempty"`
}

// maxProblemBytes bounds a field and a message as a response writes them. A
// field may be a key the request chose, which may be as long as the body.
const maxProblemBytes = 512

// problemsJSON renders problems as a response writes them. The field and the
// message go through [invisible.Shown]: the field may be a key of the
// request, and the message may quote a value from it.
func problemsJSON(problems Problems) []problemJSON {
	out := make([]problemJSON, 0, len(problems))
	for _, p := range problems {
		out = append(out, problemJSON{
			Field:   invisible.Shown(p.Field, maxProblemBytes),
			Message: invisible.Shown(p.Message, maxProblemBytes),
		})
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

// HTTP serves payments over HTTP, one account's at a time: every method takes
// the account the caller has authenticated, and answers for that account's
// payments only.
type HTTP struct {
	service  *Service
	assets   Assets
	accepted Accepted
}

// NewHTTP serves the payments of service, naming assets from assets and
// paying them to where accepted says.
func NewHTTP(service *Service, assets Assets, accepted Accepted) *HTTP {
	return &HTTP{service: service, assets: assets, accepted: accepted}
}

// Create opens a payment for account from the body of r and answers with it,
// or answers with what was wrong with the body. The error is a dependency not
// reached, with nothing written: the caller answers for that, in the one way
// it answers for every dependency.
func (h *HTTP) Create(w http.ResponseWriter, r *http.Request, account AccountID) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", nil)
		return nil
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid",
			Problems{{Message: "the body ended before the request said it would"}})
		return nil
	}
	req, problems := readRequest(body, h.assets)
	if problems != nil {
		writeError(w, http.StatusBadRequest, "invalid", problems)
		return nil
	}
	destination, accepted, err := h.accepted.Destination(r.Context(), account, req.asset)
	if err != nil {
		return err
	}
	if !accepted {
		writeError(w, http.StatusBadRequest, "invalid", Problems{{Field: "asset",
			Message: "not accepted by this account: nothing says where a payment of it is paid to"}})
		return nil
	}
	p, err := h.service.Open(r.Context(), account, Request{
		Amount:      req.amount,
		Destination: destination,
		Metadata:    req.metadata,
		ExpiresAt:   req.expiresAt,
	})
	var found Problems
	if errors.As(err, &found) {
		writeError(w, http.StatusBadRequest, "invalid", found)
		return nil
	}
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/payments/"+p.ID().String())
	writeJSON(w, http.StatusCreated, bodyJSON(p))
	return nil
}

// Read answers with the payment of account the path's id names, or that
// there is none. An identifier of another account's payment, of no payment,
// and of no shape are answered alike, so that the answer says nothing about
// what exists. The error is as for [HTTP.Create].
func (h *HTTP) Read(w http.ResponseWriter, r *http.Request, account AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	p, err := h.service.Find(r.Context(), account, id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", nil)
		return nil
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusOK, bodyJSON(p))
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
	}
}

// Announce is the event a payment produces on reaching the status it is in,
// carrying the payment as a read of it would answer. What a merchant is told
// and what a merchant can ask for are then the same thing.
//
// The payload is written without escaping the characters that have a meaning
// in a page, because it is not one: it goes to a merchant's endpoint. Escaping
// them spends six bytes where the value had one, and a payment carrying
// metadata at every bound this package allows would come to more than an event
// is allowed to be.
func Announce(p *Payment) (Event, error) {
	var payload bytes.Buffer
	writer := json.NewEncoder(&payload)
	writer.SetEscapeHTML(false)
	if err := writer.Encode(bodyJSON(p)); err != nil {
		return Event{}, fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	return Event{
		Name:    "payment." + p.Status().String(),
		Payload: bytes.TrimRight(payload.Bytes(), "\n"),
	}, nil
}

// writeError answers with the one shape every failure has: a word for what
// went wrong, and for a body that was refused, what was wrong with it.
func writeError(w http.ResponseWriter, status int, word string, problems Problems) {
	writeJSON(w, status, errorJSON{Error: word, Problems: problemsJSON(problems)})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already written, so a failed encode cannot become an
	// error response.
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // nothing to report it to
}
