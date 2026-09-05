package payment_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// An amount arrives as text from whoever is asking for a payment.
func FuzzParseMoney(f *testing.F) {
	for _, seed := range []string{
		"", "0", "1", "-1", "+1", "1.5", "1e18",
		"1000000000000000000", strings.Repeat("9", 78), strings.Repeat("9", 200),
		"\uff10", "\x00", " 1 ",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, amount string) {
		asset, err := payment.NewAsset("polygon", "r", "JPYC", 18)
		if err != nil {
			t.Fatal(err)
		}

		m, err := payment.ParseMoney(asset, amount)
		if err != nil {
			return
		}

		if !m.IsSet() {
			t.Errorf("ParseMoney(%q) returned no error and no amount", amount)
		}
		if m.Amount().Sign() < 0 {
			t.Errorf("ParseMoney(%q) read a negative amount", amount)
		}
		// What a person is shown is the same number as what is stored. The
		// digits either side of the point, put back together and padded to
		// the asset's places, are the smallest-unit amount. Round-tripping
		// Amount().String() instead would only check that big.Int can read
		// what it wrote.
		shown, _, _ := strings.Cut(m.String(), " ")
		whole, fraction, hasPoint := strings.Cut(shown, ".")
		if !hasPoint {
			fraction = ""
		}
		if places := int(asset.Decimals()); len(fraction) > places {
			t.Fatalf("%q renders %d places, more than the asset's %d", amount, len(fraction), places)
		} else {
			fraction += strings.Repeat("0", places-len(fraction))
		}
		rebuilt := strings.TrimLeft(whole+fraction, "0")
		if rebuilt == "" {
			rebuilt = "0"
		}
		if rebuilt != m.Amount().String() {
			t.Errorf("ParseMoney(%q) stores %s and shows %q, which is %s",
				amount, m.Amount(), m.String(), rebuilt)
		}
		if invisible.Has(m.String()) {
			t.Errorf("ParseMoney(%q) renders as %q, which a terminal would act on", amount, m.String())
		}
	})
}

// FuzzParseUnits reads what a request writes for an amount, and checks what
// was stored and what Units shows against the input, each worked out without
// ParseUnits or Units.
func FuzzParseUnits(f *testing.F) {
	for _, seed := range []string{
		"", "0", "0.0", "1", "1000", "1000.5", "1000.500", "007.50", "0.000000000000000001",
		"-1", "+1", "1e3", "1,000", " 1", "1.", ".5", ".", "１", "\x00",
		strings.Repeat("9", 60), strings.Repeat("9", 61), "1." + strings.Repeat("0", 19),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, amount string) {
		asset, err := payment.NewAsset("polygon", "r", "JPYC", 18)
		if err != nil {
			t.Fatal(err)
		}

		m, err := payment.ParseUnits(asset, amount)
		if err != nil {
			return
		}

		if !m.IsSet() {
			t.Errorf("ParseUnits(%q) returned no error and no amount", amount)
		}
		if m.Amount().Sign() < 0 {
			t.Errorf("ParseUnits(%q) read a negative amount", amount)
		}
		if digits := len(m.Amount().String()); digits > payment.MaxAmountDigits {
			t.Errorf("ParseUnits(%q) read %d digits, more than the %d it admits", amount, digits, payment.MaxAmountDigits)
		}
		// Rebuilt from the input rather than read back through ParseUnits and
		// Units: a round trip would still pass if both misplaced the point the
		// same way.
		whole, fraction, _ := strings.Cut(amount, ".")
		places := int(asset.Decimals())
		if len(fraction) > places {
			t.Fatalf("ParseUnits(%q) read %d places, more than the asset's %d", amount, len(fraction), places)
		}
		stored := strings.TrimLeft(whole+fraction+strings.Repeat("0", places-len(fraction)), "0")
		if stored == "" {
			stored = "0"
		}
		if got := m.Amount().String(); got != stored {
			t.Errorf("ParseUnits(%q) stored %s, want %s", amount, got, stored)
		}
		want := strings.TrimLeft(whole, "0")
		if want == "" {
			want = "0"
		}
		if fraction = strings.TrimRight(fraction, "0"); fraction != "" {
			want += "." + fraction
		}
		shown := m.Units()
		if shown != want {
			t.Errorf("ParseUnits(%q) is shown as %q, want %q", amount, shown, want)
		}
		if invisible.Has(shown) {
			t.Errorf("ParseUnits(%q) renders as %q, which a terminal would act on", amount, shown)
		}
	})
}

// Metadata is whatever a merchant attaches, and it is stored, shown in a
// console and sent in a webhook.
func FuzzMetadata(f *testing.F) {
	for _, seed := range []string{
		"", "order", "A-1", "customer name", "a\nb", "a\u200bb",
		strings.Repeat("k", 1000), "\xff", "\x00",
	} {
		f.Add(seed, seed)
	}

	// The seeds above pair a key with itself, so a value-only check would
	// never be reached: the key is refused first.
	f.Add("k", "a\nb")
	f.Add("k", "a\u200bb")

	f.Fuzz(func(t *testing.T, key, value string) {
		r := request(t)
		r.Metadata = map[string]string{key: value}

		p, err := payment.New(r, now)
		if err != nil {
			return
		}

		// What was accepted comes back as it went in. Dropping an entry
		// silently would otherwise satisfy every check below.
		back := p.Metadata()
		if got, ok := back[key]; !ok || got != value {
			t.Errorf("metadata %q = %q was stored as %q (present: %v)", key, value, got, ok)
		}
		if len(back) != 1 {
			t.Errorf("one entry went in and %d came back", len(back))
		}
		for k, v := range back {
			if invisible.Has(k) || invisible.Has(v) {
				t.Errorf("a payment holds metadata a terminal would act on: %q = %q", k, v)
			}
			if len(k) > payment.MaxMetadataKeyBytes || len(v) > payment.MaxMetadataValueSize {
				t.Errorf("a payment holds metadata beyond what it will store: %d and %d bytes", len(k), len(v))
			}
		}
		if p.Status() != payment.Created {
			t.Errorf("a new payment is %s", p.Status())
		}
		if !p.ExpiresAt().After(p.CreatedAt()) {
			t.Errorf("a payment expires at %s, before it was created at %s", p.ExpiresAt(), p.CreatedAt())
		}
	})
}

// An account is written the way its own chain writes one, so this package can
// only say what holds anything at all.
func FuzzParseAddress(f *testing.F) {
	for _, seed := range []string{
		"", "0x" + strings.Repeat("ab", 20), "DRpbCBMxVnDK7maPM5tGv6MvB3v1sRMC86PZ8okm1GwZ",
		strings.Repeat("a", 200), "a\nb", "a\u202eb", "\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := payment.ParseAddress(s)
		if err != nil {
			return
		}

		if got.String() != s {
			t.Errorf("ParseAddress(%q) changed it to %q", s, got)
		}
		if invisible.Has(got.String()) {
			t.Errorf("ParseAddress(%q) accepted something a terminal would act on", s)
		}
		if len(got) > payment.MaxAddressBytes {
			t.Errorf("ParseAddress accepted %d bytes", len(got))
		}
	})
}
