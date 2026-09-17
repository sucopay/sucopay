package checkout

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/problem"
)

// The headers every answer under /checkout carries. The page runs nothing
// but the deployment's own script and style, reaches nothing but the
// deployment's own routes, cannot be framed, and sends no referrer: the
// token is in the URL, and the URL goes nowhere else.
var headers = map[string]string{
	"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
	"Referrer-Policy":        "no-referrer",
	"X-Content-Type-Options": "nosniff",
	"Cache-Control":          "no-store",
	// What frame-ancestors says, for a browser that reads only this.
	"X-Frame-Options": "DENY",
}

// Chains is what reading the deployment's networks has come to, in a word
// per network. Declared here, where it is called; the observers are one.
type Chains interface {
	Words() (networks, assets map[string]string)
}

// Watched says whether a network has been read at all. The cursors are one.
type Watched interface {
	Has(ctx context.Context, network payment.Network) (bool, error)
}

// Deps is what serving a page takes from the instance.
type Deps struct {
	Store    *Postgres
	Payments *payment.Postgres
	// Service is what issues an attempt and moves a payment.
	Service *payment.Service
	// Domain is the EIP-712 domain of an asset's contract: its name and
	// version, which the document lists with the asset.
	Domain func(asset payment.Asset) (name, version string)
	// Key derives tokens, and KeyID says which key.
	Key   [32]byte
	KeyID string
	// ChainIDs are the chain id of each network by the name the document
	// lists it under, for the wallet to switch to.
	ChainIDs map[string]uint64
	Chains   Chains
	Watched  Watched
	// Assets is the page's script and style, served under
	// /checkout-assets/, and nil in a deployment without them, which
	// serves the plain page. Not under /checkout/: a path there is a
	// token's, and the mux would not tell "assets" from one.
	Assets fs.FS
	Now    func() time.Time
}

// HTTP serves a payer's page: the HTML, what the page shows, and what the
// payer signs. Every method is reached by a token and by nothing else.
type HTTP struct {
	deps Deps
}

// NewHTTP serves pages from deps.
func NewHTTP(deps Deps) *HTTP {
	return &HTTP{deps: deps}
}

// found is a token resolved to its payment, with what the page needs of
// the account.
type found struct {
	owner   Owner
	payment *payment.Payment
}

// find resolves the token the path carries. Nothing found is one answer
// whatever the cause, so that an answer tells nothing about what exists.
func (h *HTTP) find(r *http.Request) (found, error) {
	token, err := ParseToken(r.PathValue("token"))
	if err != nil {
		return found{}, ErrNotFound
	}
	owner, err := h.deps.Store.Lookup(r.Context(), Hash(token), h.deps.KeyID, h.deps.Now())
	if err != nil {
		return found{}, err
	}
	// The token was derived from the id under the key in force, so a hash
	// that led here was derived by this deployment; derived again, it has
	// to be the same. Anything else is a row nothing here wrote.
	if !hmac.Equal([]byte(Derive(h.deps.Key, owner.Payment)), []byte(token)) {
		return found{}, ErrNotFound
	}
	p, _, err := h.deps.Payments.Find(r.Context(), owner.Account, owner.Payment)
	if errors.Is(err, payment.ErrNotFound) {
		return found{}, ErrNotFound
	}
	if err != nil {
		return found{}, err
	}
	return found{owner: owner, payment: p}, nil
}

// facts gathers what the state of f is built from.
func (h *HTTP) facts(ctx context.Context, f found) (Facts, error) {
	p := f.payment
	matched, err := h.deps.Store.Matched(ctx, f.owner.Account, p.ID(), p.Status())
	if err != nil {
		return Facts{}, err
	}
	watched, err := h.deps.Watched.Has(ctx, p.Network())
	if err != nil {
		return Facts{}, err
	}
	live, _, found, err := h.deps.Payments.Live(ctx, f.owner.Account, p.ID())
	if err != nil {
		return Facts{}, err
	}
	attempted, err := h.deps.Payments.Attempted(ctx, f.owner.Account, p.ID())
	if err != nil {
		return Facts{}, err
	}
	networks, _ := h.deps.Chains.Words()
	facts := Facts{
		Now:         h.deps.Now(),
		ChainID:     h.deps.ChainIDs[string(p.Network())],
		Merchant:    f.owner.Name,
		NetworkWord: networks[string(p.Network())],
		Watched:     watched,
		Matched:     matched,
		Reissued:    attempted >= payment.MaxAttempts,
	}
	if found {
		typed := h.typed(p, live)
		facts.Attempt = &typed
	}
	return facts, nil
}

// typed is the signing material of a against p.
func (h *HTTP) typed(p *payment.Payment, a *payment.Attempt) TypedData {
	name, version := h.deps.Domain(p.Asset())
	return Typed(p, a, name, version, h.deps.ChainIDs[string(p.Network())])
}

// MaxAttemptBodyBytes bounds the body of a request for an attempt, which
// is empty or names one attempt.
const MaxAttemptBodyBytes = 1 << 10

// Attempt gives the payer something to sign: the live attempt if there is
// one, or a new one. A request may instead ask for the live attempt to be
// replaced, once, by naming it under reissue; the deployment cannot see a
// key spent by something that did not pay, and takes the page's word.
//
// What refuses the request is answered before anything moves, so that a
// refused request leaves the payment as it was.
func (h *HTTP) Attempt(w http.ResponseWriter, r *http.Request) error {
	setHeaders(w)
	f, err := h.find(r)
	if errors.Is(err, ErrNotFound) {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	if err != nil {
		return err
	}
	reissue, ok := readReissue(w, r)
	if !ok {
		return nil
	}
	facts, err := h.facts(r.Context(), f)
	if err != nil {
		return err
	}
	if reissue != "" && (facts.Attempt == nil || facts.Attempt.ID != reissue) {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Field: "reissue", Message: "names no live attempt of this payment"}})
		return nil
	}
	if facts.Attempt != nil && reissue == "" {
		problem.JSON(w, http.StatusOK, facts.Attempt)
		return nil
	}
	if reason := Reason(f.payment, facts); reason != "" {
		problem.JSON(w, http.StatusConflict, map[string]string{"error": reason})
		return nil
	}
	account, p := f.owner.Account, f.payment
	if reissue != "" {
		err := h.deps.Service.Supersede(r.Context(), account, p.ID(), reissue)
		switch {
		case errors.Is(err, payment.ErrReissued):
			problem.JSON(w, http.StatusConflict, map[string]string{"error": ReasonReissued})
			return nil
		case errors.Is(err, payment.ErrNotFound), errors.Is(err, payment.ErrStale):
			// Another request of the same page retired it first. What is
			// live now is the one to sign, if there is one yet.
			return h.answerLive(w, r, account, p)
		case err != nil:
			return err
		}
	}
	if p.Status() == payment.Created {
		_, err := h.deps.Service.Await(r.Context(), account, p.ID())
		var moved payment.Problems
		if errors.Is(err, payment.ErrStale) || errors.As(err, &moved) {
			// Another request of the same page moved it first. Issuing
			// goes on: what that request issued, if anything, is answered
			// with below.
			err = nil
		}
		if err != nil {
			return err
		}
	}
	a, p, err := h.deps.Service.Issue(r.Context(), account, p.ID())
	if errors.Is(err, payment.ErrAttemptLive) {
		// Another request of the same page got there first, and its
		// attempt is the one to sign.
		return h.answerLive(w, r, account, p)
	}
	if err != nil {
		return err
	}
	typed := h.typed(p, a)
	problem.JSON(w, http.StatusCreated, typed)
	return nil
}

// answerLive answers with the live attempt of p, for a request another
// request of the same page got ahead of. With none live, the page is told
// to ask again.
func (h *HTTP) answerLive(w http.ResponseWriter, r *http.Request, account payment.AccountID, p *payment.Payment) error {
	live, _, found, err := h.deps.Payments.Live(r.Context(), account, p.ID())
	if err != nil {
		return err
	}
	if !found {
		problem.JSON(w, http.StatusConflict, map[string]string{"error": ReasonAgain})
		return nil
	}
	problem.JSON(w, http.StatusOK, h.typed(p, live))
	return nil
}

// readReissue reads the body of a request for an attempt: nothing, or an
// object naming the attempt to replace. Anything else is refused, and the
// second result says so.
func readReissue(w http.ResponseWriter, r *http.Request) (payment.AttemptID, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAttemptBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem.Refuse(w, http.StatusRequestEntityTooLarge, "too_large", nil)
		return "", false
	case err != nil:
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{Message: "body could not be read"}})
		return "", false
	case len(bytes.TrimSpace(body)) == 0:
		return "", true
	}
	var asked struct {
		Reissue *string `json:"reissue"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&asked); err != nil || dec.More() {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Message: "body is empty, or an object with reissue naming an attempt"}})
		return "", false
	}
	// An empty object asks for nothing, as an empty body does.
	if asked.Reissue == nil {
		return "", true
	}
	id := *asked.Reissue
	if !attemptIDShaped(id) {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{Field: "reissue", Message: "not an attempt id"}})
		return "", false
	}
	return payment.AttemptID(id), true
}

// attemptIDShaped says whether s has the shape of an attempt's id: 32
// lowercase hexadecimal characters, as a token's is 64.
func attemptIDShaped(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// State answers with what the page shows.
func (h *HTTP) State(w http.ResponseWriter, r *http.Request) error {
	setHeaders(w)
	f, err := h.find(r)
	if errors.Is(err, ErrNotFound) {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	if err != nil {
		return err
	}
	facts, err := h.facts(r.Context(), f)
	if err != nil {
		return err
	}
	problem.JSON(w, http.StatusOK, Build(f.payment, facts))
	return nil
}

// Page answers with the HTML the payer opens. Not found is a page too: this
// is a URL a person opens, and what they get is read by them.
func (h *HTTP) Page(w http.ResponseWriter, r *http.Request) error {
	setHeaders(w)
	f, err := h.find(r)
	if errors.Is(err, ErrNotFound) {
		return writePage(w, http.StatusNotFound, nil, h.deps.Assets != nil)
	}
	if err != nil {
		return err
	}
	facts, err := h.facts(r.Context(), f)
	if err != nil {
		return err
	}
	state := Build(f.payment, facts)
	return writePage(w, http.StatusOK, &state, h.deps.Assets != nil)
}

// Assets serves the page's script and style, when the deployment has
// them. Not under the page's headers: a script is cached, and a policy on
// a stylesheet governs nothing. What it keeps is that a file is read as
// what it says it is.
func (h *HTTP) Assets(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A directory is not an asset, and the file server would list one.
	if h.deps.Assets == nil || strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return nil
	}
	// The file server cleans the path and the fs refuses to climb out of
	// itself, which is what keeps a path from reaching past the assets.
	http.StripPrefix("/checkout-assets/", http.FileServerFS(h.deps.Assets)).ServeHTTP(w, r)
	return nil
}

func setHeaders(w http.ResponseWriter) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}
}
