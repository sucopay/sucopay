package checkout_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
)

// What the wallet is handed is the EIP-3009 authorisation for this
// payment: the destination, the amount in the smallest unit, the deadline
// in seconds, and the attempt's key as the nonce, under the asset's domain.
func TestTyped_IsTheAuthorisationForThePaymentUnderTheAssetsDomain(t *testing.T) {
	t.Parallel()
	p := opening(t, payment.AwaitingPayment, now.Add(15*time.Minute+500*time.Millisecond))
	a, err := payment.NewAttempt(p, now)
	if err != nil {
		t.Fatal(err)
	}

	typed := checkout.Typed(p, a, "JPY Coin", "1", 137)

	if typed.ID != a.ID() || typed.PrimaryType != "TransferWithAuthorization" {
		t.Errorf("id %q, type %q; want the attempt's and TransferWithAuthorization", typed.ID, typed.PrimaryType)
	}
	if d := typed.Domain; d.Name != "JPY Coin" || d.Version != "1" || d.ChainID != 137 || d.VerifyingContract != jpyc(t).Reference() {
		t.Errorf("domain = %+v, want the asset's on chain 137", d)
	}
	m := typed.Message
	if m.To != p.Destination() || m.Value != "1000"+strings.Repeat("0", 18) || m.ValidAfter != "0" {
		t.Errorf("message = %+v, want the destination, the smallest-unit amount, valid from 0", m)
	}
	// The deadline as the chain compares it: whole seconds, rounded down,
	// so that no signature outlives the payment.
	if want := now.Add(15 * time.Minute).Unix(); m.ValidBefore != itoa(want) {
		t.Errorf("validBefore = %s, want %d", m.ValidBefore, want)
	}
	if m.Nonce != "0x"+a.Key() {
		t.Errorf("nonce = %s, want the attempt's key under 0x", m.Nonce)
	}
	body, err := json.Marshal(typed)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"chainId":137`, `"verifyingContract"`, `"validBefore"`, `"primaryType"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("JSON lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), `"from"`) {
		t.Errorf("JSON carries from, which the page fills in:\n%s", body)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
