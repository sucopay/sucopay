package payment_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// awaiting is a payment an attempt can be made against, with the deadline the
// test gives it.
func awaiting(t *testing.T, expires time.Time) *payment.Payment {
	t.Helper()
	amount, err := payment.ParseMoney(jpyc(t), "1000")
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: "0xshop",
		ExpiresAt:   expires,
	}, expires.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewAttempt_TakesTheKeyFromTheChainsOwnSpace(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	seen := map[string]bool{}
	for range 1000 {
		a, err := payment.NewAttempt(p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		key := a.Key()
		if len(key) != 64 {
			t.Fatalf("key is %d characters, want 64", len(key))
		}
		if strings.Trim(key, "0123456789abcdef") != "" {
			t.Fatalf("key is %q, which is not lowercase hexadecimal", key)
		}
		if seen[key] {
			t.Fatalf("key %q was issued twice", key)
		}
		seen[key] = true
	}
}

func TestNewAttempt_HoldsTheDeadlineTheChainWillCompare(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, time.March, 1, 12, 0, 0, 500_000_000, time.UTC)
	p := awaiting(t, expires)
	now := time.Date(2026, time.March, 1, 11, 30, 0, 0, time.UTC)

	a, err := payment.NewAttempt(p, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := expires.Truncate(time.Second); !a.ValidBefore().Equal(want) {
		t.Errorf("ValidBefore is %s, want %s", a.ValidBefore(), want)
	}
	if a.PaymentID() != p.ID() {
		t.Errorf("the attempt is against %s, want %s", a.PaymentID(), p.ID())
	}
	if a.Network() != p.Network() {
		t.Errorf("the attempt is on %s, want %s", a.Network(), p.Network())
	}
	if a.Scheme() != payment.EIP3009 {
		t.Errorf("the scheme is %s, want %s", a.Scheme(), payment.EIP3009)
	}
	if a.Status() != payment.Issued {
		t.Errorf("the attempt is %s, want %s", a.Status(), payment.Issued)
	}
	if a.Authorizer() != "" {
		t.Errorf("the authorizer is %q, and nobody has signed yet", a.Authorizer())
	}
	if !a.CreatedAt().Equal(now) {
		t.Errorf("CreatedAt is %s, want %s", a.CreatedAt(), now)
	}
	// A timestamptz keeps microseconds, so an attempt made with more precision
	// than that holds what a row will hold and stays equal to itself.
	fine, err := payment.NewAttempt(p, now.Add(123456789*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(123456 * time.Microsecond); !fine.CreatedAt().Equal(want) {
		t.Errorf("CreatedAt is %s, want %s", fine.CreatedAt(), want)
	}
}

func TestNewAttempt_IdentifiesEveryAttemptOnItsOwn(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	first, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == second.ID() {
		t.Errorf("two attempts against one payment are both %s", first.ID())
	}
	for _, id := range []payment.AttemptID{first.ID(), second.ID()} {
		if len(id) != 32 || strings.Trim(string(id), "0123456789abcdef") != "" {
			t.Errorf("%q is not 32 lowercase hexadecimal characters", id)
		}
	}
}

func TestNewAttempt_RefusesAPaymentNobodyCanPay(t *testing.T) {
	t.Parallel()
	if _, err := payment.NewAttempt(nil, time.Now()); err == nil {
		t.Error("an attempt was made against no payment at all")
	}
	// A key is something a payer can spend. One against a payment that is not
	// open for payment is a key nothing should honour, so it is never minted.
	amount, err := payment.ParseMoney(jpyc(t), "1000")
	if err != nil {
		t.Fatal(err)
	}
	created, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: "0xshop",
		ExpiresAt:   time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payment.NewAttempt(created, time.Now()); err == nil {
		t.Errorf("an attempt was made against a payment that is %s", created.Status())
	}
}

func TestConfirm_MovesAnIssuedAttemptAndNamesWhoSigned(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}
	if a.Status() != payment.Confirming {
		t.Errorf("the attempt is %s, want %s", a.Status(), payment.Confirming)
	}
	if a.Authorizer() != "0xpayer" {
		t.Errorf("the authorizer is %q, want %q", a.Authorizer(), "0xpayer")
	}
	if err := a.Confirm("0xsomebodyelse"); err == nil {
		t.Error("an attempt already confirming was confirmed again")
	}
	if a.Authorizer() != "0xpayer" {
		t.Errorf("the refused move left the authorizer as %q", a.Authorizer())
	}
}

func TestConfirm_RefusesATransferNobodyAuthorized(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Confirm(""); err == nil {
		t.Error("an attempt was confirmed with no authorizer")
	}
	if a.Status() != payment.Issued {
		t.Errorf("the refused move left the attempt %s", a.Status())
	}
}

func TestRestoreAttempt_RefusesARowTheDomainWouldNotHaveWritten(t *testing.T) {
	t.Parallel()
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	sound := payment.StoredAttempt{
		ID:          "0123456789abcdef0123456789abcdef",
		PaymentID:   "0123456789abcdef0123456789abcdef",
		Scheme:      payment.EIP3009,
		Network:     "polygon",
		Key:         strings.Repeat("ab", 32),
		ValidBefore: expires,
		Status:      payment.Issued,
		CreatedAt:   time.Now(),
	}
	if _, err := payment.RestoreAttempt(sound); err != nil {
		t.Fatalf("a sound row was refused: %v", err)
	}
	cases := []struct {
		name   string
		change func(*payment.StoredAttempt)
	}{
		{"a status nothing defines", func(s *payment.StoredAttempt) { s.Status = "submitted" }},
		{"a scheme nothing defines", func(s *payment.StoredAttempt) { s.Scheme = "permit" }},
		{"a key that is not a nonce", func(s *payment.StoredAttempt) { s.Key = "not a nonce" }},
		{"an identifier of another shape", func(s *payment.StoredAttempt) { s.ID = "1" }},
		{"a payment of another shape", func(s *payment.StoredAttempt) { s.PaymentID = "1" }},
		{"no network", func(s *payment.StoredAttempt) { s.Network = "" }},
		{"an authorizer no address could be", func(s *payment.StoredAttempt) {
			s.Status, s.Authorizer = payment.Confirming, "0xpayer\u200b"
		}},
		{"confirming with nobody who signed it", func(s *payment.StoredAttempt) {
			s.Status, s.Authorizer = payment.Confirming, ""
		}},
		{"issued with somebody who signed it", func(s *payment.StoredAttempt) {
			s.Status, s.Authorizer = payment.Issued, "0xpayer"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := sound
			c.change(&row)
			if _, err := payment.RestoreAttempt(row); err == nil {
				t.Error("the row was restored")
			}
		})
	}
}

// An authorizer comes from a chain, by way of an adapter. It is checked here
// the way a destination is, rather than trusted for having arrived as an
// Address.
func TestConfirm_RefusesAnAuthorizerNoAddressCouldBe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		authorizer payment.Address
	}{
		{"nobody", ""},
		{"longer than an address is", payment.Address(strings.Repeat("a", payment.MaxAddressBytes+1))},
		{"holding a character that does not show up", "0xpayer\u200b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := awaiting(t, time.Now().Add(time.Hour))
			a, err := payment.NewAttempt(p, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Confirm(c.authorizer); err == nil {
				t.Fatalf("Confirm(%q) went through", c.authorizer)
			}
			if a.Status() != payment.Issued {
				t.Errorf("the refused move left the attempt %s", a.Status())
			}
			if a.Authorizer() != "" {
				t.Errorf("the refused move left %q as the authorizer", a.Authorizer())
			}
		})
	}
}

func TestUnconfirm_TakesAConfirmingAttemptBackAndForgetsWhoSigned(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}

	if err := a.Unconfirm(); err != nil {
		t.Fatal(err)
	}
	if a.Status() != payment.Issued {
		t.Errorf("the attempt is %s, want %s", a.Status(), payment.Issued)
	}
	if a.Authorizer() != "" {
		t.Errorf("the authorizer is %q, and nothing has been seen against this attempt", a.Authorizer())
	}
}

func TestUnconfirm_RefusesAnAttemptThatWasNeverConfirmed(t *testing.T) {
	t.Parallel()
	p := awaiting(t, time.Now().Add(time.Hour))
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Unconfirm(); err == nil {
		t.Error("an issued attempt was taken back")
	}
	if a.Status() != payment.Issued {
		t.Errorf("the refused move left the attempt %s", a.Status())
	}
}
