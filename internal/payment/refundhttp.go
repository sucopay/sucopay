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
	"slices"
	"time"

	"github.com/sucopay/sucopay/internal/problem"
)

// refundJSON is a refund as a response writes it.
//
// The key the authorisation spends is not among the fields. What needs it is
// the page the merchant signs on, which reads it under the page's token; a
// merchant's server has no use for it, and a value with no use is one more
// place for it to be written down.
type refundJSON struct {
	ID          RefundID      `json:"id"`
	Payment     ID            `json:"payment"`
	Status      RefundStatus  `json:"status"`
	Amount      string        `json:"amount"`
	Destination Address       `json:"destination"`
	ExpiresAt   time.Time     `json:"expires_at"`
	CreatedAt   time.Time     `json:"created_at"`
	Transfer    *transferJSON `json:"transfer"`
	// RefundURL is where the merchant signs, and carries the page's token. It
	// is answered to whoever held the credential and to nobody else, as a
	// payment's checkout URL is.
	RefundURL *string `json:"refund_url,omitempty"`
}

// refundKeys is every key the body of a refund request may hold.
var refundKeys = []string{"amount"}

// refundBody is a refund as a route answers with it.
func (h *HTTP) refundBody(r *Refund, t *Transfer) refundJSON {
	url := h.refunds.URL(r.ID())
	return refundJSON{
		ID: r.ID(), Payment: r.PaymentID(), Status: r.Status(),
		Amount: r.Amount().Units(), Destination: r.Destination(),
		ExpiresAt: r.ExpiresAt(), CreatedAt: r.CreatedAt(),
		Transfer: t.shown(), RefundURL: &url,
	}
}

// Refund sends back what a payment received, to the address it came from.
//
// What is sent back is the body's amount, or everything the payment has left
// when the body names none. Where it goes is not the caller's to choose: it
// is read off the transfer that paid the payment, so that a refund cannot be
// turned into a way of moving a merchant's money somewhere else.
func (h *HTTP) Refund(w http.ResponseWriter, r *http.Request, account AccountID) error {
	key, problems := readIdempotencyKey(r.Header)
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem.Refuse(w, http.StatusRequestEntityTooLarge, "too_large", nil)
		return nil
	case err != nil:
		problems = append(problems, Problem{Message: "the body ended before the request said it would"})
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(problems))
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
	// Only a payment the chain settled: what arrived is taken back when a
	// transfer vanishes, and a succeeded payment is the one kind that keeps
	// it. The store says so again under its lock, which is where a payment
	// that moved between here and there is caught.
	if p.Status() != Succeeded {
		problems = append(problems, Problem{Field: "status", Message: fmt.Sprintf(
			"payment is %s, and only a %s payment can be refunded", p.Status(), Succeeded)})
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(problems))
		return nil
	}
	// Where the money goes, and whether there is anywhere for it to go at
	// all: a payment nothing was seen for has nobody to send it back to.
	paid, seen, err := h.service.MatchedTransfer(r.Context(), account, id)
	if err != nil {
		return err
	}
	var destination Address
	if !seen {
		problems = append(problems, Problem{Field: "payment", Message: fmt.Sprintf(
			"no transfer has been seen for %s, so there is nowhere to send a refund", id)})
	} else if destination, err = ParseAddress(paid.From); err != nil {
		problems = append(problems, Problem{Field: "payment", Message: fmt.Sprintf(
			"the transfer that paid %s came from %v", id, err)})
	}
	amount, wrong := h.asked(r.Context(), account, p, body)
	problems = append(problems, wrong...)
	if problems != nil {
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(problems))
		return nil
	}
	refundID, err := NewRefundID()
	if err != nil {
		return err
	}
	var idempotency Idempotency
	if key != "" {
		sum := sha256.Sum256(body)
		idempotency = Idempotency{Key: key, BodyHash: sum[:]}
	}
	refund, err := h.service.OpenRefund(r.Context(), account, p, refundID, RefundRequest{
		Amount:      amount,
		Destination: destination,
		Token:       h.refunds.Token(refundID),
		Idempotency: idempotency,
	})
	var refused Problems
	switch {
	case errors.Is(err, ErrIdempotencyKeyUsed):
		return h.opened(w, r, account, key, idempotency.BodyHash)
	case errors.As(err, &refused):
		problem.Refuse(w, http.StatusBadRequest, "invalid", asFields(refused))
		return nil
	case err != nil:
		return err
	}
	w.Header().Set("Location", "/payments/"+id.String()+"/refunds/"+refund.ID().String())
	problem.JSON(w, http.StatusCreated, h.refundBody(refund, nil))
	return nil
}

// opened answers a request under an idempotency key this account has used
// with the refund that key opened, and refuses a body that is not the one it
// was opened with.
func (h *HTTP) opened(w http.ResponseWriter, r *http.Request, account AccountID, key string, body []byte) error {
	first, err := h.service.RefundByKey(r.Context(), account, key)
	if err != nil {
		return err
	}
	if !bytes.Equal(first.Idempotency().BodyHash, body) {
		problem.Refuse(w, http.StatusBadRequest, "invalid", []problem.Field{{
			Field:   IdempotencyKeyHeader,
			Message: "already used for another body. Send that body, or a key nothing has used"}})
		return nil
	}
	seen, err := h.seenBy(r.Context(), account, first)
	if err != nil {
		return err
	}
	w.Header().Set("Idempotent-Replayed", "true")
	w.Header().Set("Location", "/payments/"+first.PaymentID().String()+"/refunds/"+first.ID().String())
	problem.JSON(w, http.StatusCreated, h.refundBody(first, seen))
	return nil
}

// ReadRefund answers with one refund of one payment of the account.
func (h *HTTP) ReadRefund(w http.ResponseWriter, r *http.Request, account AccountID) error {
	id, err := ParseID(r.PathValue("id"))
	if err != nil {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	refundID, err := ParseRefundID(r.PathValue("refund"))
	if err != nil {
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	}
	refund, err := h.service.Refund(r.Context(), account, id, refundID)
	switch {
	case errors.Is(err, ErrNotFound):
		problem.Refuse(w, http.StatusNotFound, "not_found", nil)
		return nil
	case err != nil:
		return err
	}
	seen, err := h.seenBy(r.Context(), account, refund)
	if err != nil {
		return err
	}
	problem.JSON(w, http.StatusOK, h.refundBody(refund, seen))
	return nil
}

// seenBy is the transfer seen for a refund, and nil where none has been.
func (h *HTTP) seenBy(ctx context.Context, account AccountID, r *Refund) (*Transfer, error) {
	t, found, err := h.service.RefundTransfer(ctx, account, r.PaymentID(), r.ID())
	if err != nil || !found {
		return nil, err
	}
	return &t, nil
}

// asked is the amount a request asks to send back: what the body names, or
// everything the payment has left when it names none.
func (h *HTTP) asked(ctx context.Context, account AccountID, p *Payment, body []byte) (Money, Problems) {
	// A request that asks for everything has nothing to say, and may say it
	// as an empty body or as an empty object.
	raw := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(body)) > 0 {
		var err error
		if raw, err = decodeObject(body); err != nil {
			return Money{}, Problems{{Message: err.Error()}}
		}
	}
	var problems Problems
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if slices.Contains(refundKeys, key) {
			continue
		}
		if key == "" {
			key = `""`
		}
		problems = append(problems, Problem{Field: key, Message: "unknown key"})
	}
	written, given := raw["amount"]
	if !given {
		left, err := h.service.Refundable(ctx, account, p)
		if err != nil {
			return Money{}, Problems{{Field: "amount", Message: err.Error()}}
		}
		return left, problems
	}
	var units string
	if err := json.Unmarshal(written, &units); err != nil || string(written) == "null" {
		return Money{}, append(problems, Problem{Field: "amount", Message: fmt.Sprintf(
			"want a string, got %s", kindOf(written))})
	}
	amount, err := parseUnits(units, p.Asset().Decimals())
	if err != nil {
		return Money{}, append(problems, Problem{Field: "amount", Message: err.Error()})
	}
	money, err := NewMoney(p.Asset(), amount)
	if err != nil {
		return Money{}, append(problems, Problem{Field: "amount", Message: err.Error()})
	}
	return money, problems
}
