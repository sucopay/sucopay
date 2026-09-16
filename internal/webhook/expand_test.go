package webhook_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/webhook"
)

// paid puts a payment of account in place, and one outbox row about it,
// as the payment's own store writes them.
func paid(t *testing.T, pool *pgxpool.Pool, account payment.AccountID, id string, event string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		insert into payments (id, account_id, asset_network, asset_reference, asset_symbol,
		                      asset_decimals, amount, received, destination, status, created_at, expires_at)
		values ($1, $2, 'polygon', 'r', 'JPYC', 18, 1, 1, '0xabc', 'succeeded', $3::timestamptz, $3::timestamptz + interval '1 hour')
		on conflict do nothing`, id, account, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		insert into outbox (account_id, payment_id, event, payload, created_at)
		values ($1, $2, $3, $4, $5)`,
		account, id, event, `{"id":"`+id+`","status":"succeeded"}`, now); err != nil {
		t.Fatal(err)
	}
}

// deploymentEndpoint is the operator's endpoint, which nothing registers
// yet: a row put in as the route that will issue one would.
func deploymentEndpoint(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		insert into webhook_endpoints (id, scope, url, description, enabled, secret, key_id, created_at)
		values ($1, 'deployment', 'https://ops.example/in', '', true, '\x00', 'k1', $2)`, id, now); err != nil {
		t.Fatal(err)
	}
}

type deliveryRow struct {
	ID       webhook.ID
	Endpoint webhook.ID
	Account  payment.AccountID
	Payment  string
	Type     string
	State    string
	Body     map[string]any
}

func deliveries(t *testing.T, pool *pgxpool.Pool) []deliveryRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		select id, endpoint_id, account_id, coalesce(payment_id, ''), type, state, payload
		  from webhook_deliveries order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []deliveryRow
	for rows.Next() {
		var r deliveryRow
		var body []byte
		if err := rows.Scan(&r.ID, &r.Endpoint, &r.Account, &r.Payment, &r.Type, &r.State, &body); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &r.Body); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func outboxRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var p1 = strings.Repeat("1", 32)

func TestExpand_MakesOneDeliveryPerEndpointThatReceivesTheEventAndRemovesTheRow(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	all, _ := created(t, s, first, "https://hooks.example/all")
	expired, _, err := s.Create(t.Context(), first,
		webhook.Registration{URL: "https://hooks.example/expired", Events: []string{"payment.expired"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.Update(t.Context(), first, all.ID, webhook.Changes{Events: new([]string)}); err != nil {
		t.Fatal(err)
	}
	disabled, _ := created(t, s, first, "https://hooks.example/off")
	if _, err := s.Update(t.Context(), first, disabled.ID, webhook.Changes{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	others, _ := created(t, s, other, "https://hooks.example/other")
	deploymentEndpoint(t, pool, "deployment")
	paid(t, pool, first, p1, "payment.succeeded")

	taken, err := s.Expand(t.Context(), now, 10)

	if err != nil || taken != 1 {
		t.Fatalf("Expand = %d, %v; want the one row", taken, err)
	}
	if outboxRows(t, pool) != 0 {
		t.Error("the outbox row is still there")
	}
	got := deliveries(t, pool)
	to := map[webhook.ID]bool{}
	for _, d := range got {
		to[d.Endpoint] = true
		if d.Account != first || d.Payment != p1 || d.Type != "payment.succeeded" || d.State != webhook.Pending {
			t.Errorf("delivery %+v, want the payment's account and event, pending", d)
		}
		if d.Body["type"] != "payment.succeeded" || d.Body["account"] != string(first) {
			t.Errorf("body %v, want type and the payment's account", d.Body)
		}
		if data, _ := d.Body["data"].(map[string]any); data["id"] != p1 {
			t.Errorf("data %v, want the payment", d.Body["data"])
		}
	}
	if len(got) != 2 || !to[all.ID] || !to["deployment"] {
		t.Errorf("deliveries went to %v, want the endpoint receiving everything and the deployment's", to)
	}
	if to[expired.ID] || to[disabled.ID] || to[others.ID] {
		t.Error("a delivery went to an endpoint that does not receive the event")
	}
}

func TestExpand_RemovesARowNoEndpointReceivesWithoutADelivery(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	paid(t, pool, first, p1, "payment.succeeded")

	taken, err := s.Expand(t.Context(), now, 10)

	if err != nil || taken != 1 || outboxRows(t, pool) != 0 || len(deliveries(t, pool)) != 0 {
		t.Errorf("Expand = %d, %v with %d rows left and %d deliveries; want the row gone and nothing made",
			taken, err, outboxRows(t, pool), len(deliveries(t, pool)))
	}
}

func TestDue_SendsOnePaymentsEventsInOrderAndNoMoreThanFourToAnEndpoint(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	e, _, err := s.Create(t.Context(), first, webhook.Registration{URL: "https://hooks.example/in"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"payment.awaiting_payment", "attempt.confirming", "payment.succeeded"} {
		paid(t, pool, first, p1, event)
	}
	for i := range 5 {
		paid(t, pool, first, strings.Repeat(string(rune('2'+i)), 32), "payment.succeeded")
	}
	if _, err := s.Expand(t.Context(), now, 100); err != nil {
		t.Fatal(err)
	}

	due, err := s.Due(t.Context(), now, 32, 4)

	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 4 {
		t.Fatalf("Due = %d deliveries, want four to the one endpoint", len(due))
	}
	seen := map[payment.ID]int{}
	for _, d := range due {
		seen[d.Delivery.Payment]++
		if d.URL != e.URL || d.Delivery.Endpoint != e.ID {
			t.Errorf("due %+v, want the endpoint's URL", d)
		}
	}
	if seen[payment.ID(p1)] != 1 || due[0].Delivery.Payment != payment.ID(p1) || due[0].Delivery.Type != "payment.awaiting_payment" {
		t.Errorf("the payment with three events has %d due, first %s; want its first event alone", seen[payment.ID(p1)], due[0].Delivery.Type)
	}
	// Deliver the first, and the second of the same payment comes due.
	if err := s.Attempted(t.Context(), due[0].Delivery, webhook.Outcome{Status: http.StatusOK}, now, func() float64 { return 0 }); err != nil {
		t.Fatal(err)
	}
	again, err := s.Due(t.Context(), now, 32, 4)
	if err != nil {
		t.Fatal(err)
	}
	var next string
	for _, d := range again {
		if d.Delivery.Payment == payment.ID(p1) {
			next = d.Delivery.Type
		}
	}
	if next != "attempt.confirming" {
		t.Errorf("after the first was delivered, %q is due for the payment; want its second", next)
	}
	// Nothing is due to a disabled endpoint.
	off := false
	if _, err := s.Update(t.Context(), first, e.ID, webhook.Changes{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if none, err := s.Due(t.Context(), now, 32, 4); err != nil || len(none) != 0 {
		t.Errorf("Due to a disabled endpoint = %d, %v; want none", len(none), err)
	}
}

// A backlog to one endpoint longer than a round does not keep another
// endpoint's delivery out of the round.
func TestDue_ReadsAnotherEndpointsDeliveryPastABacklogLongerThanARound(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	busy, _ := created(t, s, first, "https://hooks.example/busy")
	quiet, _ := created(t, s, other, "https://hooks.example/quiet")
	for i := range 40 {
		paid(t, pool, first, fmt.Sprintf("%032d", i), "payment.succeeded")
	}
	paid(t, pool, other, strings.Repeat("9", 32), "payment.succeeded")
	if _, err := s.Expand(t.Context(), now, 100); err != nil {
		t.Fatal(err)
	}

	due, err := s.Due(t.Context(), now, 32, 4)

	if err != nil {
		t.Fatal(err)
	}
	to := map[webhook.ID]int{}
	for _, d := range due {
		to[d.Delivery.Endpoint]++
	}
	if to[busy.ID] != 4 || to[quiet.ID] != 1 {
		t.Errorf("due went %d to the busy endpoint and %d to the quiet one, want 4 and 1", to[busy.ID], to[quiet.ID])
	}
}

func TestAttempted_WritesTheAttemptAndLeavesTheDeliveryDeliveredDueAgainOrFailed(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	created(t, s, first, "https://hooks.example/in")
	paid(t, pool, first, p1, "payment.succeeded")
	if _, err := s.Expand(t.Context(), now, 1); err != nil {
		t.Fatal(err)
	}
	due, err := s.Due(t.Context(), now, 1, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("Due = %d, %v", len(due), err)
	}
	d := due[0].Delivery
	none := func() float64 { return 0 }

	if err := s.Attempted(t.Context(), d, webhook.Outcome{Status: 500, Response: "boom", Took: 1500 * time.Millisecond}, now, none); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	var next *time.Time
	read := func() {
		t.Helper()
		if err := pool.QueryRow(t.Context(), `select state, attempts, next_at from webhook_deliveries where id = $1`, d.ID).Scan(&state, &attempts, &next); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if state != webhook.Pending || attempts != 1 || next == nil || !next.Equal(now.Add(5*time.Second)) {
		t.Errorf("after one failure: %s, %d attempts, next %v; want pending again in 5 seconds", state, attempts, next)
	}
	var status *int
	var reason, response string
	var tookMs int
	if err := pool.QueryRow(t.Context(), `select status, reason, response, took_ms from webhook_attempts where delivery_id = $1`, d.ID).Scan(&status, &reason, &response, &tookMs); err != nil {
		t.Fatal(err)
	}
	if status == nil || *status != 500 || reason != "" || response != "boom" || tookMs != 1500 {
		t.Errorf("attempt = %v %q %q %d, want the 500 and its body", status, reason, response, tookMs)
	}
	// Every attempt after that fails until the tenth, which fails the delivery.
	for i := 2; i <= webhook.MaxAttempts; i++ {
		d.Attempts = i - 1
		if err := s.Attempted(t.Context(), d, webhook.Outcome{Reason: webhook.ReasonTimeout}, now, none); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if state != webhook.Failed || attempts != webhook.MaxAttempts || next != nil {
		t.Errorf("after %d failures: %s, %d attempts, next %v; want failed with no next", webhook.MaxAttempts, state, attempts, next)
	}
	if err := pool.QueryRow(t.Context(), `select status from webhook_attempts where delivery_id = $1 and reason = 'timeout' limit 1`, d.ID).Scan(&status); err != nil || status != nil {
		t.Errorf("an attempt with no answer has status %v, %v; want null", status, err)
	}
	// A delivered one.
	paid(t, pool, first, strings.Repeat("2", 32), "payment.succeeded")
	if _, err := s.Expand(t.Context(), now, 1); err != nil {
		t.Fatal(err)
	}
	due, _ = s.Due(t.Context(), now, 1, 1)
	d = due[0].Delivery
	if err := s.Attempted(t.Context(), d, webhook.Outcome{Status: 204}, now, none); err != nil {
		t.Fatal(err)
	}
	read()
	var deliveredAt *time.Time
	_ = pool.QueryRow(t.Context(), `select delivered_at from webhook_deliveries where id = $1`, d.ID).Scan(&deliveredAt)
	if state != webhook.Delivered || attempts != 1 || next != nil || deliveredAt == nil {
		t.Errorf("after a 204: %s, %d attempts, next %v, delivered %v; want delivered", state, attempts, next, deliveredAt)
	}
}

// An allowance the operator mistyped allows nothing, and stops nothing
// else: the delivery is read, and its check refuses the inside address.
func TestDue_ReadsAMistypedAllowanceAsNoneAndGoesOn(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	e, _ := created(t, s, first, "https://hooks.example/in")
	if _, err := pool.Exec(t.Context(), `update webhook_endpoints set allowed = '{"10.0.5.0/24","not a prefix"}' where id = $1`, e.ID); err != nil {
		t.Fatal(err)
	}
	paid(t, pool, first, p1, "payment.succeeded")
	if _, err := s.Expand(t.Context(), now, 1); err != nil {
		t.Fatal(err)
	}

	due, err := s.Due(t.Context(), now, 32, 4)

	if err != nil || len(due) != 1 || len(due[0].Allowed) != 1 {
		t.Errorf("Due = %d, %v with %v allowed; want the delivery with the one prefix that reads", len(due), err, due)
	}
	if allowed, err := s.Allowed(t.Context(), e.ID); err != nil || len(allowed) != 1 {
		t.Errorf("Allowed = %v, %v; want the one prefix that reads", allowed, err)
	}
}

func TestSweep_RemovesDeliveredAndFailedPastTheRetentionAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	created(t, s, first, "https://hooks.example/in")
	for i := range 4 {
		paid(t, pool, first, fmt.Sprintf("%032d", i), "payment.succeeded")
	}
	if _, err := s.Expand(t.Context(), now, 10); err != nil {
		t.Fatal(err)
	}
	rows := deliveries(t, pool)
	for i, state := range []string{"delivered", "failed", "pending", "delivered"} {
		age := now.Add(-webhook.Retention - time.Hour)
		if i == 3 {
			age = now.Add(-webhook.Retention + time.Hour)
		}
		if _, err := pool.Exec(t.Context(), `update webhook_deliveries set state = $2, created_at = $3, next_at = null where id = $1`, rows[i].ID, state, age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `insert into webhook_attempts (delivery_id, at, status, took_ms) values ($1, $2, 200, 1)`, rows[i].ID, age); err != nil {
			t.Fatal(err)
		}
	}

	swept, err := s.Sweep(t.Context(), now.Add(-webhook.Retention), 1000)

	if err != nil || swept != 2 {
		t.Fatalf("Sweep = %d, %v; want the two old finished ones", swept, err)
	}
	left := deliveries(t, pool)
	if len(left) != 2 || left[0].State != webhook.Pending || left[1].State != webhook.Delivered {
		t.Errorf("left %+v, want the old pending one and the recent delivered one", left)
	}
	var attempts int
	if err := pool.QueryRow(t.Context(), `select count(*) from webhook_attempts`).Scan(&attempts); err != nil || attempts != 2 {
		t.Errorf("attempts left = %d, %v; want the two of the deliveries kept", attempts, err)
	}
}
