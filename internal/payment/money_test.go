package payment_test

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/payment"
)

// one is one JPYC in its smallest unit. The asset has 18 decimals, so this is
// the number an amount has to hold and int64 cannot.
const one = "1000000000000000000"

func TestNewMoney_HoldsAnAmountLargerThanInt64(t *testing.T) {
	t.Parallel()
	thousand, ok := new(big.Int).SetString("1000"+strings.Repeat("0", 18), 10)
	if !ok {
		t.Fatal("the test's own number did not parse")
	}
	if thousand.IsInt64() {
		t.Fatal("this amount fits in an int64, so it does not test what it claims")
	}

	m, err := payment.NewMoney(jpyc(t), thousand)
	if err != nil {
		t.Fatal(err)
	}

	if got := m.Amount(); got.Cmp(thousand) != 0 {
		t.Errorf("amount = %s, want %s", got, thousand)
	}
}

func TestNewMoney_RefusesWhatIsNotAnAmount(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		asset  payment.Asset
		amount *big.Int
		want   error
	}{
		{"no asset", payment.Asset{}, big.NewInt(1), payment.ErrNoAsset},
		{"no amount", jpyc(t), nil, payment.ErrNoAmount},
		{"below zero", jpyc(t), big.NewInt(-1), payment.ErrNegativeAmount},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := payment.NewMoney(c.asset, c.amount)

			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestNewMoney_CopiesTheAmountSoACallerCannotChangeIt(t *testing.T) {
	t.Parallel()
	amount := big.NewInt(100)
	m, err := payment.NewMoney(jpyc(t), amount)
	if err != nil {
		t.Fatal(err)
	}

	amount.SetInt64(999)

	if got := m.Amount(); got.Int64() != 100 {
		t.Errorf("amount = %s, want 100: the caller's number reached inside", got)
	}
}

func TestMoney_AmountReturnsACopy(t *testing.T) {
	t.Parallel()
	m, err := payment.ParseMoney(jpyc(t), one)
	if err != nil {
		t.Fatal(err)
	}

	m.Amount().SetInt64(1)

	if got := m.Amount().String(); got != one {
		t.Errorf("amount = %s, want %s: what Amount returned was the amount itself", got, one)
	}
}

func TestParseMoney_RefusesWhatIsNotAWholeNumber(t *testing.T) {
	t.Parallel()
	for _, amount := range []string{"", "1.5", "1e18", "abc", " 1", "1 "} {
		t.Run(amount, func(t *testing.T) {
			if _, err := payment.ParseMoney(jpyc(t), amount); err == nil {
				t.Errorf("ParseMoney(%q) returned no error", amount)
			}
		})
	}
}

func TestParseMoney_RefusesAnAmountLongerThanAnyAmountCouldBe(t *testing.T) {
	t.Parallel()
	// Reading base ten costs more than linearly in the length of the input,
	// and the string comes from whoever is asking for a payment.
	long := strings.Repeat("9", payment.MaxAmountDigits+1)

	if _, err := payment.ParseMoney(jpyc(t), long); err == nil {
		t.Fatalf("read a %d character amount", len(long))
	}
	if _, err := payment.ParseMoney(jpyc(t), long[1:]); err != nil {
		t.Errorf("refused an amount of the greatest length it allows: %v", err)
	}
}

func TestMoney_RefusesArithmeticAcrossTwoTokensThatShareASymbol(t *testing.T) {
	t.Parallel()
	// The reason assets are not symbols. Adding these would be adding two
	// different tokens because they spell themselves the same.
	native, err := payment.ParseMoney(usdc(t), "1000000")
	if err != nil {
		t.Fatal(err)
	}
	bridged, err := payment.ParseMoney(bridgedUSDC(t), "1000000")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := native.Add(bridged); !errors.Is(err, payment.ErrDifferentAssets) {
		t.Errorf("Add: err = %v, want ErrDifferentAssets", err)
	}
	if _, err := native.Cmp(bridged); !errors.Is(err, payment.ErrDifferentAssets) {
		t.Errorf("Cmp: err = %v, want ErrDifferentAssets", err)
	}
}

func TestMoney_RefusesArithmeticAcrossTwoAssets(t *testing.T) {
	t.Parallel()
	yen, err := payment.ParseMoney(jpyc(t), "100")
	if err != nil {
		t.Fatal(err)
	}
	dollars, err := payment.ParseMoney(usdc(t), "100")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := yen.Add(dollars); !errors.Is(err, payment.ErrDifferentAssets) {
		t.Errorf("Add: err = %v, want ErrDifferentAssets", err)
	}
	if _, err := yen.Cmp(dollars); !errors.Is(err, payment.ErrDifferentAssets) {
		t.Errorf("Cmp: err = %v, want ErrDifferentAssets", err)
	}
}

func TestMoney_RefusesArithmeticWhenOneTokenIsDescribedTwoWays(t *testing.T) {
	t.Parallel()
	// One reference, two decimal counts. The sum would mean two things at
	// once, so neither description is picked over the other.
	eighteen, err := payment.ParseMoney(asset(t, "polygon", "jpyc-contract", "JPYC", 18), one)
	if err != nil {
		t.Fatal(err)
	}
	six, err := payment.ParseMoney(asset(t, "polygon", "jpyc-contract", "JPYC", 6), one)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := eighteen.Add(six); err == nil {
		t.Fatal("added an amount to itself under two descriptions")
	}
}

func TestMoney_AddsAndComparesWithinOneToken(t *testing.T) {
	t.Parallel()
	single, err := payment.ParseMoney(jpyc(t), one)
	if err != nil {
		t.Fatal(err)
	}
	double, err := payment.ParseMoney(jpyc(t), "2"+strings.Repeat("0", 18))
	if err != nil {
		t.Fatal(err)
	}

	sum, err := single.Add(single)
	if err != nil {
		t.Fatal(err)
	}
	if cmp, err := sum.Cmp(double); err != nil || cmp != 0 {
		t.Errorf("one + one = %s, want %s (cmp %d, err %v)", sum, double, cmp, err)
	}
	if cmp, err := single.Cmp(double); err != nil || cmp != -1 {
		t.Errorf("one.Cmp(two) = %d, want -1 (err %v)", cmp, err)
	}
}

func TestMoney_StringPutsTheDecimalPointBack(t *testing.T) {
	t.Parallel()
	// The same digits mean different amounts under different assets, which is
	// why the smallest unit is not what a person is shown.
	for _, c := range []struct {
		name   string
		asset  payment.Asset
		amount string
		want   string
	}{
		{"one JPYC", jpyc(t), one, "1"},
		{"one USDC", usdc(t), "1000000", "1"},
		{"the same digits as USDC", usdc(t), one, "1000000000000"},
		{"less than one", jpyc(t), "500000000000000000", "0.5"},
		{"the smallest unit", jpyc(t), "1", "0.000000000000000001"},
		{"nothing", jpyc(t), "0", "0"},
		{"an asset that does not divide", asset(t, "polygon", "r", "WHOLE", 0), "7", "7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := payment.ParseMoney(c.asset, c.amount)
			if err != nil {
				t.Fatal(err)
			}

			got := m.String()

			if !strings.HasPrefix(got, c.want+" ") {
				t.Errorf("String() = %q, want it to begin %q", got, c.want)
			}
			if !strings.Contains(got, c.asset.Symbol()) {
				t.Errorf("String() = %q, want it to name the asset", got)
			}
		})
	}
}

func TestMoney_TheZeroValueIsNotAnAmountOfAnything(t *testing.T) {
	t.Parallel()
	var m payment.Money

	if m.IsSet() {
		t.Error("the zero Money says it is set")
	}
	if _, err := m.Add(m); !errors.Is(err, payment.ErrNoAsset) {
		t.Errorf("adding the zero Money: err = %v, want ErrNoAsset", err)
	}
	if got := m.Amount().Sign(); got != 0 {
		t.Errorf("the zero Money has amount sign %d, want 0", got)
	}
}

func TestParseUnits_ReadsAnAmountInTheAssetsOwnUnits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		asset  payment.Asset
		amount string
		stored string
		shown  string
	}{
		{"a whole number", jpyc(t), "1000", "1000" + strings.Repeat("0", 18), "1000"},
		{"a fraction", jpyc(t), "1000.5", "10005" + strings.Repeat("0", 17), "1000.5"},
		{"trailing zeros", jpyc(t), "1000.500", "10005" + strings.Repeat("0", 17), "1000.5"},
		{"the smallest unit", jpyc(t), "0.000000000000000001", "1", "0.000000000000000001"},
		{"nothing", jpyc(t), "0", "0", "0"},
		{"nothing, with a point", jpyc(t), "0.0", "0", "0"},
		{"leading zeros", usdc(t), "007.50", "7500000", "7.5"},
		{"an asset that does not divide", asset(t, "polygon", "r", "WHOLE", 0), "7", "7", "7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := payment.ParseUnits(c.asset, c.amount)
			if err != nil {
				t.Fatal(err)
			}

			if got := m.Amount().String(); got != c.stored {
				t.Errorf("ParseUnits(%q) stores %s, want %s", c.amount, got, c.stored)
			}
			if got := m.Units(); got != c.shown {
				t.Errorf("ParseUnits(%q).Units() = %q, want %q", c.amount, got, c.shown)
			}
			if !m.Asset().Same(c.asset) {
				t.Errorf("ParseUnits(%q) is of %v, want %v", c.amount, m.Asset(), c.asset)
			}
		})
	}
}

func TestParseUnits_TwoSpellingsOfOneAmountAreOneMoney(t *testing.T) {
	t.Parallel()
	a, err := payment.ParseUnits(jpyc(t), "1000.5")
	if err != nil {
		t.Fatal(err)
	}
	b, err := payment.ParseUnits(jpyc(t), "1000.500")
	if err != nil {
		t.Fatal(err)
	}

	if cmp, err := a.Cmp(b); err != nil || cmp != 0 {
		t.Errorf("Cmp = %d, %v; want 0 and no error", cmp, err)
	}
}

func TestParseUnits_RefusesWhatIsNotDigitsWithAPoint(t *testing.T) {
	t.Parallel()
	for _, amount := range []string{
		"-1", "+1", "1e3", "1,000", " 1", "1 ", "１", "", ".", "1.", ".5", "1.5.0", "0x10",
	} {
		t.Run(amount, func(t *testing.T) {
			_, err := payment.ParseUnits(jpyc(t), amount)

			if err == nil {
				t.Fatalf("ParseUnits(%q) returned no error", amount)
			}
			// The refusal says what an amount is made of, so that a person
			// who wrote a sign or an exponent learns what to write.
			if !strings.Contains(err.Error(), "digits") {
				t.Errorf("ParseUnits(%q) = %v, want a refusal that says what an amount is made of", amount, err)
			}
		})
	}
}

func TestParseUnits_RefusesMorePlacesThanTheAssetDivides(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		asset payment.Asset
		ok    string
		over  string
	}{
		{"18 places", jpyc(t), "1." + strings.Repeat("0", 17) + "1", "1." + strings.Repeat("0", 18) + "1"},
		{"6 places", usdc(t), "1.000001", "1.0000001"},
		{"no places", asset(t, "polygon", "r", "WHOLE", 0), "1", "1.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := payment.ParseUnits(c.asset, c.ok); err != nil {
				t.Errorf("ParseUnits(%q) = %v, want the amount", c.ok, err)
			}
			if _, err := payment.ParseUnits(c.asset, c.over); err == nil {
				t.Errorf("ParseUnits(%q) returned no error, want a refusal", c.over)
			}
		})
	}
}

func TestParseUnits_RefusesAnAmountLongerThanAnyAmountCouldBe(t *testing.T) {
	t.Parallel()
	// The count that matters is of digits in the smallest unit, once the
	// fraction is filled out to the asset's places: 60 digits before the
	// point and 18 after make 78 with JPYC.
	whole := strings.Repeat("9", payment.MaxAmountDigits-int(jpyc(t).Decimals()))

	m, err := payment.ParseUnits(jpyc(t), whole)
	if err != nil {
		t.Fatalf("refused an amount of the greatest length it allows: %v", err)
	}
	if got := len(m.Amount().String()); got != payment.MaxAmountDigits {
		t.Errorf("stored %d digits, want %d", got, payment.MaxAmountDigits)
	}
	if _, err := payment.ParseUnits(jpyc(t), "1"+whole); err == nil {
		t.Errorf("read an amount of %d digits in the smallest unit", payment.MaxAmountDigits+1)
	}
	// A refusal is read on a screen and in a response, where a string of
	// any length the sender chose has no place.
	long := strings.Repeat("0", 200)
	_, err = payment.ParseUnits(jpyc(t), long)
	if err == nil {
		t.Fatal("read an amount of more characters than any amount has")
	}
	if strings.Contains(err.Error(), long) {
		t.Errorf("the refusal repeats all %d characters: %v", len(long), err)
	}
}

func TestParseUnits_RefusesAnAmountOfNoAsset(t *testing.T) {
	t.Parallel()
	if _, err := payment.ParseUnits(payment.Asset{}, "1"); err == nil {
		t.Error("ParseUnits of no asset returned no error")
	}
}

func TestMoney_UnitsSaysWhenThereIsNoAmount(t *testing.T) {
	t.Parallel()
	var m payment.Money

	if got := m.Units(); got != "no amount" {
		t.Errorf("the zero Money's Units() = %q, want it to say there is none", got)
	}
}
