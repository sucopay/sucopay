package payment

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// MaxAmountDigits bounds what [ParseMoney] will read. Parsing base ten costs
// more than linearly in the length of the input, and the string comes from
// whoever is asking for a payment. Seventy eight digits is more than any
// token's total supply in its smallest unit.
const MaxAmountDigits = 78

// Money is an amount of one asset, held in that asset's smallest unit.
//
// JPYC has 18 decimals, so an amount does not fit in int64 and a float cannot
// hold one exactly. The zero Money is not an amount of anything: only a value
// from [NewMoney] is usable, and the methods say so rather than guessing.
type Money struct {
	// amount is never nil in a value NewMoney returned, and is never mutated
	// after it is set. Every method that would change it returns a new Money.
	amount *big.Int
	asset  Asset
}

var (
	// ErrNoAsset reports a Money that was never given an asset.
	ErrNoAsset = errors.New("money has no asset")
	// ErrNoAmount reports a Money that was never given an amount.
	ErrNoAmount = errors.New("money has no amount")
	// ErrDifferentAssets reports arithmetic across two assets.
	ErrDifferentAssets = errors.New("money of different assets")
	// ErrNegativeAmount reports an amount below zero.
	ErrNegativeAmount = errors.New("amount is negative")
)

// NewMoney returns an amount of asset in that asset's smallest unit. The
// amount is copied, so a caller may go on using the value it passed.
func NewMoney(asset Asset, amount *big.Int) (Money, error) {
	switch {
	case !asset.IsSet():
		return Money{}, ErrNoAsset
	case amount == nil:
		return Money{}, fmt.Errorf("%s: %w", asset, ErrNoAmount)
	case amount.Sign() < 0:
		return Money{}, fmt.Errorf("%s: %w", asset, ErrNegativeAmount)
	}
	return Money{amount: new(big.Int).Set(amount), asset: asset}, nil
}

// ParseMoney reads an amount written in the asset's smallest unit.
func ParseMoney(asset Asset, amount string) (Money, error) {
	if len(amount) > MaxAmountDigits {
		return Money{}, fmt.Errorf("%s: amount is %d characters, at most %d",
			asset, len(amount), MaxAmountDigits)
	}
	n, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return Money{}, fmt.Errorf("%s: %q is not a whole number", asset, amount)
	}
	return NewMoney(asset, n)
}

// Asset returns which token the amount is of.
func (m Money) Asset() Asset { return m.asset }

// Amount returns the amount in the asset's smallest unit. The result is a copy
// that callers may modify.
func (m Money) Amount() *big.Int {
	if m.amount == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(m.amount)
}

// IsSet reports whether m came from [NewMoney] rather than being the zero
// value.
func (m Money) IsSet() bool { return m.asset.IsSet() && m.amount != nil }

// String renders the amount in the asset's own units, followed by the asset.
// The symbol in it is a label rather than an identifier, so two amounts that
// read alike are not necessarily of one token.
func (m Money) String() string {
	if !m.IsSet() {
		return "no amount"
	}
	return m.units() + " " + m.asset.String()
}

// units renders the amount with the asset's decimal point put back in. The
// smallest unit is what is stored and what is compared; this is only for
// showing a person, who does not think in units of 10^-18.
func (m Money) units() string {
	digits := m.amount.String()
	places := int(m.asset.decimals)
	if places == 0 {
		return digits
	}
	if len(digits) <= places {
		digits = strings.Repeat("0", places-len(digits)+1) + digits
	}
	whole, fraction := digits[:len(digits)-places], digits[len(digits)-places:]
	if fraction = strings.TrimRight(fraction, "0"); fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

// Add returns the sum. Two amounts of different assets have no sum, so this
// reports an error rather than a number nobody can spend.
func (m Money) Add(other Money) (Money, error) {
	if err := m.comparable(other); err != nil {
		return Money{}, err
	}
	return Money{amount: new(big.Int).Add(m.amount, other.amount), asset: m.asset}, nil
}

// Cmp compares two amounts of one asset, returning -1, 0 or 1 as m is less
// than, equal to, or greater than other.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.comparable(other); err != nil {
		return 0, err
	}
	return m.amount.Cmp(other.amount), nil
}

// comparable reports whether two amounts are of one token.
//
// Assets are compared by [Asset.Same], which is network and reference. Two
// descriptions of one token that disagree about its decimals would make the
// sum mean two things at once, so that is refused as well rather than picked
// between.
func (m Money) comparable(other Money) error {
	switch {
	case !m.IsSet() || !other.IsSet():
		return ErrNoAsset
	case !m.asset.Same(other.asset):
		return fmt.Errorf("%w: %s and %s", ErrDifferentAssets, m.asset, other.asset)
	case m.asset.decimals != other.asset.decimals:
		return fmt.Errorf("%s: described with %d decimals and with %d",
			m.asset, m.asset.decimals, other.asset.decimals)
	}
	return nil
}
