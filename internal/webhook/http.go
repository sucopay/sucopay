package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/problem"
)

// maxEventsListed bounds the entries of an events list. There are fewer
// types than this, so a longer list names something more than once or
// names types there are not, and either is answered without walking it.
const maxEventsListed = 32

// MaxBodyBytes bounds the body of a request. A registration is a URL, a
// note and a short list, which fits in a fraction of it.
const MaxBodyBytes = 64 << 10

// HTTP serves endpoints over HTTP, one account's at a time: every method
// takes the account the caller has authenticated, and answers for that
// account's endpoints only.
type HTTP struct {
	store    *Postgres
	resolver Resolver
	now      func() time.Time
}

// NewHTTP serves the endpoints in store, checking destinations through
// resolver.
func NewHTTP(store *Postgres, resolver Resolver, now func() time.Time) *HTTP {
	return &HTTP{store: store, resolver: resolver, now: now}
}

// endpointJSON is an endpoint as a response writes it.
type endpointJSON struct {
	ID          ID        `json:"id"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	// Secret is written once, in the answer to the registration, and left
	// out of every other answer.
	Secret string `json:"secret,omitempty"`
}

func bodyJSON(e Endpoint) endpointJSON {
	return endpointJSON{ID: e.ID, URL: e.URL, Description: e.Description, Events: e.Events,
		Enabled: e.Enabled, CreatedAt: e.CreatedAt}
}

// Create registers an endpoint for account from the body of r and answers
// with it and its secret, or with what was wrong with the body. The error
// is a dependency not reached, with nothing written: the caller answers for
// that.
func (h *HTTP) Create(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	raw, ok := h.body(w, r, []string{"url", "description", "events"})
	if !ok {
		return nil
	}
	var reg Registration
	problems := read(raw, "url", &reg.URL, true)
	problems = append(problems, read(raw, "description", &reg.Description, false)...)
	events, eventProblems := readEvents(raw)
	problems = append(problems, eventProblems...)
	reg.Events = events
	problems = append(problems, checkDescription(reg.Description)...)
	if len(problems) > 0 {
		problem.Refuse(w, http.StatusBadRequest, "invalid", problems)
		return nil
	}
	if _, refused := Check(r.Context(), h.resolver, reg.URL, nil); refused != nil {
		problem.Refuse(w, http.StatusBadRequest, "invalid", fields(refused))
		return nil
	}
	e, secret, err := h.store.Create(r.Context(), account, reg, h.now())
	if errors.Is(err, ErrTooMany) {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Message: fmt.Sprintf("already holds %d endpoints, which is as many as an account may hold", MaxEndpoints)}})
		return nil
	}
	if err != nil {
		return err
	}
	body := bodyJSON(e)
	body.Secret = string(secret)
	w.Header().Set("Location", "/webhook_endpoints/"+e.ID.String())
	problem.JSON(w, http.StatusCreated, body)
	return nil
}

// List answers with every endpoint of account.
func (h *HTTP) List(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	endpoints, err := h.store.List(r.Context(), account)
	if err != nil {
		return err
	}
	out := make([]endpointJSON, 0, len(endpoints))
	for _, e := range endpoints {
		out = append(out, bodyJSON(e))
	}
	problem.JSON(w, http.StatusOK, map[string]any{"endpoints": out})
	return nil
}

// Read answers with the endpoint of account the path's id names, or that
// there is none. Another account's, none, and an id of no shape are answered
// alike.
func (h *HTTP) Read(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	e, ok, err := h.find(w, r, account)
	if !ok || err != nil {
		return err
	}
	problem.JSON(w, http.StatusOK, bodyJSON(e))
	return nil
}

// Update changes what the body of r names on one endpoint of account, and
// answers with the endpoint as it now is.
func (h *HTTP) Update(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return nil
	}
	raw, ok := h.body(w, r, []string{"url", "description", "events", "enabled"})
	if !ok {
		return nil
	}
	var changes Changes
	var problems []problem.Field
	if _, given := raw["url"]; given {
		var u string
		problems = append(problems, read(raw, "url", &u, true)...)
		changes.URL = &u
	}
	if _, given := raw["description"]; given {
		var d string
		problems = append(problems, read(raw, "description", &d, false)...)
		problems = append(problems, checkDescription(d)...)
		changes.Description = &d
	}
	if _, given := raw["events"]; given {
		events, eventProblems := readEvents(raw)
		problems = append(problems, eventProblems...)
		changes.Events = &events
	}
	if v, given := raw["enabled"]; given {
		var enabled bool
		if err := json.Unmarshal(v, &enabled); err != nil || string(v) == "null" {
			problems = append(problems, problem.Field{Field: "enabled", Message: "want true or false, got " + kindOf(v)})
		}
		changes.Enabled = &enabled
	}
	if len(problems) > 0 {
		problem.Refuse(w, http.StatusBadRequest, "invalid", problems)
		return nil
	}
	if changes.URL != nil {
		// That the endpoint is this account's is settled before its allowance
		// is read: refused for the URL or not found is otherwise an answer
		// about another account's allowance.
		if _, err := h.store.Get(r.Context(), account, id); errors.Is(err, ErrNotFound) {
			notFound(w)
			return nil
		} else if err != nil {
			return err
		}
		allowed, err := h.store.Allowed(r.Context(), id)
		if err != nil {
			return err
		}
		if _, refused := Check(r.Context(), h.resolver, *changes.URL, allowed); refused != nil {
			problem.Refuse(w, http.StatusBadRequest, "invalid", fields(refused))
			return nil
		}
	}
	e, err := h.store.Update(r.Context(), account, id, changes)
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return nil
	}
	if err != nil {
		return err
	}
	problem.JSON(w, http.StatusOK, bodyJSON(e))
	return nil
}

// Rotate replaces the secret of one endpoint of account and answers with
// the new one, shown this once.
func (h *HTTP) Rotate(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return nil
	}
	secret, err := h.store.Rotate(r.Context(), account, id, h.now())
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return nil
	}
	if err != nil {
		return err
	}
	problem.JSON(w, http.StatusOK, map[string]string{"secret": string(secret)})
	return nil
}

// Delete removes one endpoint of account.
func (h *HTTP) Delete(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return nil
	}
	err = h.store.Delete(r.Context(), account, id, h.now())
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return nil
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Test makes one delivery of endpoint.test to one endpoint of account, and
// answers that it is on its way, with the delivery's id. Whether it arrived
// is what the delivery's attempts say, once the worker has sent it.
func (h *HTTP) Test(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return nil
	}
	delivery, err := h.store.Test(r.Context(), account, id, h.now())
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return nil
	}
	if errors.Is(err, ErrDisabled) {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Field: "enabled", Message: "the endpoint is disabled, and receives nothing until it is enabled"}})
		return nil
	}
	if err != nil {
		return err
	}
	problem.JSON(w, http.StatusAccepted, map[string]string{"delivery": delivery.String()})
	return nil
}

// find reads the endpoint the path names, answering for the ones it cannot.
func (h *HTTP) find(w http.ResponseWriter, r *http.Request, account payment.AccountID) (Endpoint, bool, error) {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return Endpoint{}, false, nil
	}
	e, err := h.store.Get(r.Context(), account, id)
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return Endpoint{}, false, nil
	}
	if err != nil {
		return Endpoint{}, false, err
	}
	return e, true, nil
}

// fields is problems as [problem.Refuse] writes them.
func fields(problems []Problem) []problem.Field {
	out := make([]problem.Field, 0, len(problems))
	for _, p := range problems {
		out = append(out, problem.Field{Field: p.Field, Message: p.Message})
	}
	return out
}

func notFound(w http.ResponseWriter) {
	problem.Refuse(w, http.StatusNotFound, "not_found", nil)
}

// body reads r as a JSON object holding no key but the ones known, answering
// for a body that is not one.
func (h *HTTP) body(w http.ResponseWriter, r *http.Request, known []string) (map[string]json.RawMessage, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		problem.Refuse(w, http.StatusRequestEntityTooLarge, "too_large", nil)
		return nil, false
	}
	if err != nil {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{Message: "body could not be read"}})
		return nil, false
	}
	raw, err := decodeObject(body)
	if err != nil {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{Message: err.Error()}})
		return nil, false
	}
	var problems []problem.Field
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if !slices.Contains(known, key) {
			problems = append(problems, problem.Field{Field: key, Message: "unknown key"})
		}
	}
	if len(problems) > 0 {
		problem.Refuse(w, http.StatusBadRequest, "invalid", problems)
		return nil, false
	}
	return raw, true
}

// read takes key as a string into out. A key that must be there and is not
// is a problem; one that may be left out leaves out as it was.
func read(raw map[string]json.RawMessage, key string, out *string, required bool) []problem.Field {
	v, ok := raw[key]
	if !ok {
		if required {
			return []problem.Field{{Field: key, Message: "required"}}
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil || string(v) == "null" {
		return []problem.Field{{Field: key, Message: "want a string, got " + kindOf(v)}}
	}
	*out = s
	return nil
}

// readEvents takes events as a list of the types an endpoint may receive,
// with repeats dropped. Left out, or null, is every type.
func readEvents(raw map[string]json.RawMessage) ([]string, []problem.Field) {
	v, ok := raw["events"]
	if !ok || string(v) == "null" {
		return nil, nil
	}
	var events []string
	if err := json.Unmarshal(v, &events); err != nil {
		return nil, []problem.Field{{Field: "events", Message: "want a list of strings, got " + kindOf(v)}}
	}
	if len(events) > maxEventsListed {
		return nil, []problem.Field{{Field: "events", Message: fmt.Sprintf("lists more than %d entries", maxEventsListed)}}
	}
	var problems []problem.Field
	out := make([]string, 0, len(events))
	for i, e := range events {
		if !slices.Contains(Events, e) {
			problems = append(problems, problem.Field{Field: fmt.Sprintf("events[%d]", i),
				Message: "not an event type: " + invisible.Shown(e, 64)})
			continue
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 && problems == nil {
		problems = append(problems, problem.Field{Field: "events", Message: "names no event type; leave it out to receive every type"})
	}
	return out, problems
}

func checkDescription(d string) []problem.Field {
	switch {
	case len(d) > MaxDescriptionBytes:
		return []problem.Field{{Field: "description", Message: fmt.Sprintf("longer than %d bytes", MaxDescriptionBytes)}}
	case invisible.Has(d):
		return []problem.Field{{Field: "description", Message: "carries a character a reader cannot see"}}
	}
	return nil
}

// decodeObject reads a body as a JSON object, or says what it is instead.
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
		return nil, errors.New("body is not JSON")
	case raw == nil:
		return nil, errors.New("body is null, want a JSON object")
	}
	if dec.More() {
		return nil, errors.New("body carries more than one JSON value")
	}
	return raw, nil
}

func kindOf(v json.RawMessage) string {
	if len(v) == 0 {
		return "nothing"
	}
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
