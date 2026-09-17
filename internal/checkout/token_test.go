package checkout_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
)

var (
	one = payment.ID(strings.Repeat("1", 32))
	two = payment.ID(strings.Repeat("2", 32))
)

func TestDerive_GivesEachPaymentItsOwnTokenAndTheSameOneEveryTime(t *testing.T) {
	t.Parallel()
	key, other := [32]byte{1}, [32]byte{2}

	token := checkout.Derive(key, one)

	if again := checkout.Derive(key, one); again != token {
		t.Error("the same key and payment gave two tokens")
	}
	if checkout.Derive(key, two) == token || checkout.Derive(other, one) == token {
		t.Error("another payment or another key gave the same token")
	}
	if _, err := checkout.ParseToken(string(token)); err != nil {
		t.Errorf("a derived token does not parse: %v", err)
	}
}

func TestParseToken_RefusesWhatIsNotSixtyFourLowercaseHexCharacters(t *testing.T) {
	t.Parallel()
	for what, s := range map[string]string{
		"short":     strings.Repeat("a", 63),
		"long":      strings.Repeat("a", 65),
		"uppercase": strings.Repeat("A", 64),
		"not hex":   strings.Repeat("g", 64),
		"empty":     "",
	} {
		if _, err := checkout.ParseToken(s); err == nil {
			t.Errorf("%s was taken for a token", what)
		}
	}
}

func TestToken_ShowsNothingToFmtOrSlog(t *testing.T) {
	t.Parallel()
	token := checkout.Derive([32]byte{3}, one)
	var out bytes.Buffer
	slog.New(slog.NewJSONHandler(&out, nil)).Info("made", "token", token)

	for what, rendered := range map[string]string{
		"%v": fmt.Sprintf("%v", token), "%s": fmt.Sprintf("%s", token), "slog": out.String(),
	} {
		if strings.Contains(rendered, string(token)) {
			t.Errorf("%s shows the token: %s", what, rendered)
		}
	}
}

func TestLinks_WritesTheURLAndTheRowsPartFromOneToken(t *testing.T) {
	t.Parallel()
	links := checkout.NewLinks([32]byte{4}, "k1", "https://pay.example")

	url := links.CheckoutURL(one)
	kept := links.Checkout(one)

	token := strings.TrimPrefix(url, "https://pay.example/checkout/")
	parsed, err := checkout.ParseToken(token)
	if err != nil || len(token) == len(url) {
		t.Fatalf("URL = %s, want the base, /checkout/ and a token", url)
	}
	if !bytes.Equal(kept.Hash, checkout.Hash(parsed)) || kept.KeyID != "k1" {
		t.Errorf("the row's part = %x %q, want the hash of the URL's token under k1", kept.Hash, kept.KeyID)
	}
	if bytes.Contains(kept.Hash, []byte(token)) || len(kept.Hash) != 32 {
		t.Errorf("hash = %x, want 32 bytes carrying nothing of the token", kept.Hash)
	}
	if slashed := checkout.NewLinks([32]byte{4}, "k1", "https://pay.example/").CheckoutURL(one); slashed != url {
		t.Errorf("a base URL with a trailing slash gave %s, want %s", slashed, url)
	}
}
