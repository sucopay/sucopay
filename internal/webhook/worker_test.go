package webhook_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/webhook"
)

// A round end to end: an outbox row becomes a delivery, the delivery is
// sent to a receiver that verifies it, and the delivery is delivered.
func TestWorker_RoundDeliversWhatThePaymentsProducedToAReceiverThatVerifiesIt(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e, secret := created(t, s, first, rc.url("example.com"))
	if _, err := pool.Exec(t.Context(), `update webhook_endpoints set allowed = '{127.0.0.1/32}' where id = $1`, e.ID); err != nil {
		t.Fatal(err)
	}
	paid(t, pool, first, p1, "payment.succeeded")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := webhook.NewWorker(s, sending(rc), observe.NewLeases(pool), quiet, time.Now)

	if err := w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := deliveries(t, pool)
	if len(got) != 1 || got[0].State != webhook.Delivered {
		t.Fatalf("deliveries = %+v, want one delivered", got)
	}
	received := rc.received()
	if len(received) != 1 {
		t.Fatalf("the receiver got %d requests, want one", len(received))
	}
	r := received[0]
	if !verify(secret, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), rc.bodies[0]) {
		t.Error("the receiver does not verify the delivery under the secret it was shown")
	}
	if !strings.Contains(string(rc.bodies[0]), `"type": "payment.succeeded"`) && !strings.Contains(string(rc.bodies[0]), `"type":"payment.succeeded"`) {
		t.Errorf("body = %s, want the event", rc.bodies[0])
	}
	var status int
	if err := pool.QueryRow(t.Context(), `select status from webhook_attempts where delivery_id = $1`, got[0].ID).Scan(&status); err != nil || status != http.StatusNoContent {
		t.Errorf("attempt status = %d, %v; want 204", status, err)
	}
}

// A receiver that answers 500 leaves the delivery pending for the next
// interval, with the attempt written down.
func TestWorker_RoundLeavesAFailedSendDueAgain(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "not now")
	})
	e, _ := created(t, s, first, rc.url("example.com"))
	if _, err := pool.Exec(t.Context(), `update webhook_endpoints set allowed = '{127.0.0.1/32}' where id = $1`, e.ID); err != nil {
		t.Fatal(err)
	}
	paid(t, pool, first, p1, "payment.succeeded")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := webhook.NewWorker(s, sending(rc), observe.NewLeases(pool), quiet, time.Now)

	if err := w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := deliveries(t, pool)
	if len(got) != 1 || got[0].State != webhook.Pending {
		t.Fatalf("deliveries = %+v, want one still pending", got)
	}
	var attempts int
	var next time.Time
	if err := pool.QueryRow(t.Context(), `select attempts, next_at from webhook_deliveries where id = $1`, got[0].ID).Scan(&attempts, &next); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(next); attempts != 1 || wait < 4*time.Second || wait > 6*time.Second {
		t.Errorf("attempts = %d, next in %v; want one attempt and about 5 seconds", attempts, wait)
	}
	var response string
	if err := pool.QueryRow(t.Context(), `select response from webhook_attempts where delivery_id = $1`, got[0].ID).Scan(&response); err != nil || response != "not now" {
		t.Errorf("attempt response = %q, %v; want what the receiver said", response, err)
	}
	// A second round before the interval sends nothing.
	if err := w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(rc.received()) != 1 {
		t.Errorf("the receiver got %d requests, want the second round to wait", len(rc.received()))
	}
}

// An endpoint whose destination no longer passes its check gets an attempt
// saying so, and nothing about where it led.
func TestWorker_RoundWritesADestinationThatFailsItsCheckAsAnAttemptWithoutTheAddress(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	created(t, s, first, rc.url("example.com"))
	paid(t, pool, first, p1, "payment.succeeded")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := webhook.NewWorker(s, sending(rc), observe.NewLeases(pool), quiet, time.Now)

	if err := w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}

	var reason, response string
	if err := pool.QueryRow(t.Context(), `select reason, response from webhook_attempts`).Scan(&reason, &response); err != nil {
		t.Fatal(err)
	}
	if reason != webhook.ReasonDestination || strings.Contains(response, "127.0.0.1") {
		t.Errorf("attempt = %q %q, want the reason destination and no address", reason, response)
	}
	if len(rc.received()) != 0 {
		t.Error("the receiver was reached")
	}
}
