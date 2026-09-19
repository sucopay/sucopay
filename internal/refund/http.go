package refund

import (
	"context"
	"crypto/hmac"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/problem"
)

// The headers every answer under /refund carries. The page runs nothing but
// the deployment's own script and style, reaches nothing but the deployment's
// own routes, cannot be framed, and sends no referrer: the token is in the
// URL, and the URL goes nowhere else.
var headers = map[string]string{
	"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
	"Referrer-Policy":        "no-referrer",
	"X-Content-Type-Options": "nosniff",
	"Cache-Control":          "no-store",
	// What frame-ancestors says, for a browser that reads only this.
	"X-Frame-Options": "DENY",
}

// Refunds is what a page reads of a refund and the payment it is against.
// Declared here, where it is called; [payment.Postgres] is one.
type Refunds interface {
	FindRefund(ctx context.Context, account payment.AccountID, id payment.ID, refund payment.RefundID) (*payment.Refund, payment.Revision, error)
	Find(ctx context.Context, account payment.AccountID, id payment.ID) (*payment.Payment, payment.Revision, error)
	RefundTransfer(ctx context.Context, account payment.AccountID, id payment.ID, refund payment.RefundID) (payment.Transfer, bool, error)
}

// Deps is what serving a page takes from the instance.
type Deps struct {
	Store   *Postgres
	Refunds Refunds
	// Domain is the EIP-712 domain of an asset's contract: its name and
	// version, which the document lists with the asset.
	Domain func(asset payment.Asset) (name, version string)
	// Key derives tokens, and KeyID says which key.
	Key   [32]byte
	KeyID string
	// ChainIDs are the chain id of each network by the name the document
	// lists it under, for the wallet to switch to.
	ChainIDs map[string]uint64
	// Assets is the page's script and style, served under /refund-assets/,
	// and nil in a deployment without them, which serves the plain page.
	Assets fs.FS
	Now    func() time.Time
}

// HTTP serves the merchant's signing page: the HTML, and what the page shows.
// Every method is reached by a token and by nothing else.
type HTTP struct {
	deps Deps
}

// NewHTTP serves pages from deps.
func NewHTTP(deps Deps) *HTTP {
	return &HTTP{deps: deps}
}

// found is a token resolved to its refund, with the payment it is against.
type found struct {
	refund  *payment.Refund
	payment *payment.Payment
	account payment.AccountID
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
	// The token was derived from the identifier under the key in force, so a
	// hash that led here was derived by this deployment; derived again, it has
	// to be the same. Anything else is a row nothing here wrote.
	if !hmac.Equal([]byte(Derive(h.deps.Key, owner.Refund)), []byte(token)) {
		return found{}, ErrNotFound
	}
	refund, _, err := h.deps.Refunds.FindRefund(r.Context(), owner.Account, owner.Payment, owner.Refund)
	if errors.Is(err, payment.ErrNotFound) {
		return found{}, ErrNotFound
	}
	if err != nil {
		return found{}, err
	}
	p, _, err := h.deps.Refunds.Find(r.Context(), owner.Account, owner.Payment)
	if errors.Is(err, payment.ErrNotFound) {
		return found{}, ErrNotFound
	}
	if err != nil {
		return found{}, err
	}
	return found{refund: refund, payment: p, account: owner.Account}, nil
}

// facts gathers what the state of f is built from.
func (h *HTTP) facts(ctx context.Context, f found) (Facts, error) {
	seen, has, err := h.deps.Refunds.RefundTransfer(ctx, f.account, f.refund.PaymentID(), f.refund.ID())
	if err != nil {
		return Facts{}, err
	}
	facts := Facts{
		Now:     h.deps.Now(),
		ChainID: h.deps.ChainIDs[string(f.payment.Network())],
	}
	facts.Name, facts.Version = h.deps.Domain(f.payment.Asset())
	if has {
		facts.Seen = &seen
	}
	return facts, nil
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
	problem.JSON(w, http.StatusOK, Build(f.payment, f.refund, facts))
	return nil
}

// Page answers with the HTML the merchant opens. Not found is a page too:
// this is a URL a person opens, and what they get is read by them.
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
	state := Build(f.payment, f.refund, facts)
	return writePage(w, http.StatusOK, &state, h.deps.Assets != nil)
}

// Assets serves the page's script and style, when the deployment has them.
// Not under the page's headers: a script is cached, and a policy on a
// stylesheet governs nothing. What it keeps is that a file is read as what it
// says it is.
func (h *HTTP) Assets(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A directory is not an asset, and the file server would list one.
	if h.deps.Assets == nil || strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return nil
	}
	// The file server cleans the path and the fs refuses to climb out of
	// itself, which is what keeps a path from reaching past the assets.
	http.StripPrefix("/refund-assets/", http.FileServerFS(h.deps.Assets)).ServeHTTP(w, r)
	return nil
}

func setHeaders(w http.ResponseWriter) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}
}
