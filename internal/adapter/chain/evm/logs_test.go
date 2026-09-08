package evm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"golang.org/x/crypto/sha3"
)

// The contracts and accounts these logs are about.
var (
	asset      = strings.ToLower(checksummed[0])
	authorizer = strings.ToLower(checksummed[1])
	merchant   = strings.ToLower(checksummed[2])
	stranger   = strings.ToLower(checksummed[3])
	// spent is a key as a chain writes one, and as an attempt holds one
	// without the prefix.
	spent = "0x" + strings.Repeat("ab", wordBytes)
	// tx is the transaction these receipts are of.
	tx = "0x" + strings.Repeat("cd", hashBytes)
	// fees is the contract Polygon charges through. Its logs are in every
	// receipt on that chain and in none on any other.
	fees = "0x0000000000000000000000000000000000001010"
)

// logOf is a log as a provider writes one.
func logOf(from string, carried []string, data, in string) string {
	quoted := make([]string, len(carried))
	for i, topic := range carried {
		quoted[i] = strconv.Quote(topic)
	}
	return fmt.Sprintf(`{"address":%q,"topics":[%s],"data":%q,"transactionHash":%q}`,
		from, strings.Join(quoted, ","), data, in)
}

// used is the log a token writes when a key is spent.
func used(from, by, key, in string) string {
	return logOf(from, []string{authorizationUsed, wordOf(by), key}, "0x", in)
}

// sent is the log a token writes when it moves.
func sent(from, out, to, value, in string) string {
	return logOf(from, []string{transferred, wordOf(out), wordOf(to)}, value, in)
}

// wordOf is an address as a log carries one: in the low bytes of a word.
func wordOf(address string) string {
	return "0x000000000000000000000000" + strings.ToLower(address)[2:]
}

// unitsOf is an amount as a transfer's log carries one.
func unitsOf(t *testing.T, decimal string) string {
	t.Helper()
	units, ok := new(big.Int).SetString(decimal, 10)
	if !ok {
		t.Fatalf("%q is not a number", decimal)
	}
	return "0x" + hex.EncodeToString(units.FillBytes(make([]byte, wordBytes)))
}

// receiptOf is a receipt as a provider writes one, in the fields this reads.
func receiptOf(of string, height uint64, logs ...string) string {
	return fmt.Sprintf(`{"transactionHash":%q,"blockHash":%q,"blockNumber":"0x%x","logs":[%s]}`,
		of, hashOf(height), height, strings.Join(logs, ","))
}

// paid is the receipt of one payment, with the fee logs Polygon puts around
// it: the payer's key is spent, and the token moves from the payer to the
// merchant.
func paid(t *testing.T, height uint64) string {
	t.Helper()
	return receiptOf(tx, height,
		logOf(fees, []string{transferred, wordOf(stranger), wordOf(merchant)}, unitsOf(t, "1"), tx),
		used(asset, authorizer, spent, tx),
		sent(asset, authorizer, merchant, unitsOf(t, "20000000000000000000000"), tx),
		logOf(fees, []string{transferred, wordOf(merchant), wordOf(stranger)}, unitsOf(t, "1"), tx),
	)
}

func TestReceipt_PairsAnAuthorizationWithTheTransferThatFollowsIt(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(paid(t, 0x89)),
		"eth_getBlockByNumber 0x89": answered(blockOf(0x89, 1788265269)),
	})

	transfers, err := evm.Receipt(t.Context(), tx)

	if err != nil {
		t.Fatal(err)
	}
	want := []chain.Transfer{{
		Scheme:     "eip3009",
		Asset:      asset,
		Key:        strings.Repeat("ab", wordBytes),
		Authorizer: authorizer,
		From:       authorizer,
		To:         merchant,
		Value:      "20000000000000000000000",
		Tx:         tx,
		Position:   0,
		Block:      chain.Block{Height: 0x89, Hash: hashOf(0x89), Parent: hashOf(0x88), Time: time.Unix(1788265269, 0).UTC()},
	}}
	if !reflect.DeepEqual(transfers, want) {
		t.Errorf("the receipt read as %+v, want %+v", transfers, want)
	}

	again, err := evm.Receipt(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, transfers) {
		t.Errorf("the same receipt read as %+v the second time", again)
	}
}

func TestReceipt_ReadsOnePairForEachAuthorizationOneTransactionCarried(t *testing.T) {
	t.Parallel()
	second := "0x" + strings.Repeat("12", wordBytes)
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(receiptOf(tx, 0x89,
			used(asset, authorizer, spent, tx),
			sent(asset, authorizer, merchant, unitsOf(t, "1"), tx),
			used(asset, stranger, second, tx),
			sent(asset, stranger, merchant, unitsOf(t, "115792089237316195423570985008687907853269984665640564039457584007913129639935"), tx),
		)),
		"eth_getBlockByNumber 0x89": answered(blockOf(0x89, 1788265269)),
	})

	transfers, err := evm.Receipt(t.Context(), tx)

	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 2 {
		t.Fatalf("the receipt read as %d transfers, want 2", len(transfers))
	}
	for i, want := range []struct {
		key        string
		authorizer string
		value      string
	}{
		{strings.Repeat("ab", wordBytes), authorizer, "1"},
		// The widest a word holds, which is the widest amount a token can
		// move, and it is carried as digits rather than as a number.
		{strings.Repeat("12", wordBytes), stranger, "115792089237316195423570985008687907853269984665640564039457584007913129639935"},
	} {
		got := transfers[i]
		if got.Key != want.key || got.Authorizer != want.authorizer || got.From != want.authorizer || got.Value != want.value {
			t.Errorf("transfer %d read as %+v, want the key %s spent by %s for %s", i, got, want.key, want.authorizer, want.value)
		}
		if got.Position != i {
			t.Errorf("transfer %d is at position %d", i, got.Position)
		}
	}
}

// Two authorisations of one payer, both signed before either transfer was
// made. Each takes a transfer of its own, in the order the two were written:
// reading the nearest transfer twice would put one payment's amount and
// merchant against the other payment's key.
func TestReceipt_GivesNoOneTransferToTwoAuthorisationsAtOnce(t *testing.T) {
	t.Parallel()
	second := "0x" + strings.Repeat("12", wordBytes)
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(receiptOf(tx, 0x89,
			used(asset, authorizer, spent, tx),
			used(asset, authorizer, second, tx),
			sent(asset, authorizer, merchant, unitsOf(t, "1"), tx),
			sent(asset, authorizer, stranger, unitsOf(t, "2"), tx),
		)),
		"eth_getBlockByNumber 0x89": answered(blockOf(0x89, 1788265269)),
	})

	transfers, err := evm.Receipt(t.Context(), tx)

	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 2 {
		t.Fatalf("the receipt read as %d transfers, want 2", len(transfers))
	}
	for i, want := range []struct {
		key   string
		to    string
		value string
	}{
		{strings.Repeat("ab", wordBytes), merchant, "1"},
		{strings.Repeat("12", wordBytes), stranger, "2"},
	} {
		if got := transfers[i]; got.Key != want.key || got.To != want.to || got.Value != want.value {
			t.Errorf("transfer %d read as %+v, want the key %s paying %s %s", i, got, want.key, want.to, want.value)
		}
	}
}

// A transfer that came from somebody other than the account that signed is not
// what the authorisation authorised. Pairing the two would name a payer who
// never paid.
func TestReceipt_ReadsNoPairTheAuthorizationDoesNotAccountFor(t *testing.T) {
	t.Parallel()
	for name, logs := range map[string][]string{
		"a transfer from somebody else": {
			used(asset, authorizer, spent, tx),
			sent(asset, stranger, merchant, "0x"+strings.Repeat("00", wordBytes), tx),
		},
		"a transfer of another token": {
			used(asset, authorizer, spent, tx),
			sent(fees, authorizer, merchant, "0x"+strings.Repeat("00", wordBytes), tx),
		},
		"no transfer at all": {
			used(asset, authorizer, spent, tx),
		},
		"a transfer before the authorisation": {
			sent(asset, authorizer, merchant, "0x"+strings.Repeat("00", wordBytes), tx),
			used(asset, authorizer, spent, tx),
		},
		"nothing but the chain's own fees": {
			logOf(fees, []string{transferred, wordOf(stranger), wordOf(merchant)}, "0x"+strings.Repeat("00", wordBytes), tx),
		},
	} {
		t.Run(name, func(t *testing.T) {
			evm, provider := opening(t, map[string]string{
				"eth_getTransactionReceipt": answered(receiptOf(tx, 0x89, logs...)),
			})

			transfers, err := evm.Receipt(t.Context(), tx)

			if err != nil {
				t.Fatal(err)
			}
			if len(transfers) != 0 {
				t.Errorf("the receipt read as %+v", transfers)
			}
			// The block is read for the time it stamps a pair with. With no
			// pair, there is nothing to stamp.
			if calls := provider.calls(); len(calls) != 1 {
				t.Errorf("the provider was asked %v", calls)
			}
		})
	}
}

// The time comes from the block, and the block at a height is not always the
// one that was there: the receipt names which block it was in, and a pair
// stamped from another block would carry a time the chain never gave it.
func TestReceipt_RefusesABlockThatIsNoLongerTheOneTheReceiptNames(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(paid(t, 0x89)),
		"eth_getBlockByNumber 0x89": answered(fmt.Sprintf(
			`{"number":"0x89","hash":%q,"parentHash":%q,"timestamp":"0x1"}`, hashOf(0x8a), hashOf(0x88))),
	})

	if transfers, err := evm.Receipt(t.Context(), tx); err == nil {
		t.Errorf("the receipt read as %+v out of a block that moved", transfers)
	}
}

// Pairing looks behind each authorisation, so what a receipt carries decides
// the work twice over. A receipt of more logs than a transaction could pay for
// is one this reads no further into.
func TestReceipt_RefusesMoreLogsThanOneTransactionWrites(t *testing.T) {
	t.Parallel()
	for _, count := range []int{maxLogs, maxLogs + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			logs := make([]string, count)
			for i := range logs {
				logs[i] = used(asset, authorizer, spent, tx)
			}
			evm, _ := opening(t, map[string]string{
				"eth_getTransactionReceipt": answered(receiptOf(tx, 0x89, logs...)),
			})

			transfers, err := evm.Receipt(t.Context(), tx)

			if refused := err != nil; refused != (count > maxLogs) {
				t.Errorf("%d logs read as %+v, %v", count, transfers, err)
			}
		})
	}
}

// A receipt is asked for by name. One naming another transaction is not an
// answer to what was asked, and its transfers would be written down under the
// name that was.
func TestReceipt_RefusesAReceiptOfAnotherTransaction(t *testing.T) {
	t.Parallel()
	other := "0x" + strings.Repeat("ef", hashBytes)
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(receiptOf(other, 0x89,
			used(asset, authorizer, spent, other),
			sent(asset, authorizer, merchant, unitsOf(t, "1"), other),
		)),
		"eth_getBlockByNumber 0x89": answered(blockOf(0x89, 1788265269)),
	})

	if transfers, err := evm.Receipt(t.Context(), tx); err == nil {
		t.Errorf("the receipt read as %+v, and it is another transaction's", transfers)
	}
}

// A transfer names who it came from and who it went to. One that names neither
// is a transfer of some other shape, and reading it would leave a payment
// recorded as having gone nowhere.
func TestReceipt_RefusesATransferThatNamesNobody(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getTransactionReceipt": answered(receiptOf(tx, 0x89,
			used(asset, authorizer, spent, tx),
			logOf(asset, []string{transferred, wordOf(authorizer)}, unitsOf(t, "1"), tx),
		)),
	})

	if transfers, err := evm.Receipt(t.Context(), tx); err == nil {
		t.Errorf("the receipt read as %+v out of a transfer naming nobody it went to", transfers)
	}
}

func TestReceipt_RefusesWhatIsNoTransactionOnThisChain(t *testing.T) {
	t.Parallel()
	evm, provider := opening(t, map[string]string{})

	for _, reference := range []string{"", "abc", strings.Repeat("cd", hashBytes), tx + "ab"} {
		if _, err := evm.Receipt(t.Context(), reference); err == nil {
			t.Errorf("Receipt took %q", reference)
		}
	}
	if calls := provider.calls(); len(calls) != 0 {
		t.Errorf("the provider was asked %v", calls)
	}
}

func TestReceipt_RefusesATransactionTheProviderHasNoReceiptFor(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{"eth_getTransactionReceipt": answered("null")})

	if transfers, err := evm.Receipt(t.Context(), tx); err == nil {
		t.Errorf("the receipt read as %+v out of nothing", transfers)
	}
}

func TestKeys_TellsTheKeysSpentFromTheAssetsWhoseCodeChanged(t *testing.T) {
	t.Parallel()
	other := strings.ToLower(checksummed[4])
	second := "0x" + strings.Repeat("12", wordBytes)
	evm, provider := opening(t, map[string]string{
		"eth_getLogs": answered("[" + strings.Join([]string{
			used(asset, authorizer, spent, tx),
			logOf(other, []string{upgraded, wordOf(stranger)}, "0x", hashOf(2)),
			used(other, stranger, second, hashOf(3)),
		}, ",") + "]"),
	})

	scan, err := evm.Keys(t.Context(), 0x10, 0x1f, []string{asset, other})

	if err != nil {
		t.Fatal(err)
	}
	want := chain.Scan{
		Consumed: []chain.Consumed{
			{Key: strings.Repeat("ab", wordBytes), Tx: tx},
			{Key: strings.Repeat("12", wordBytes), Tx: hashOf(3)},
		},
		Changed: []string{other},
	}
	if !reflect.DeepEqual(scan, want) {
		t.Errorf("the span read as %+v, want %+v", scan, want)
	}
	filter := map[string]any{
		"fromBlock": "0x10",
		"toBlock":   "0x1f",
		"address":   []any{asset, other},
		"topics":    []any{[]any{authorizationUsed, upgraded}},
	}
	if params := provider.calls()[0].params; len(params) != 1 || !reflect.DeepEqual(params[0], filter) {
		t.Errorf("the provider was asked for %v, want %v", params, filter)
	}
}

// An asset whose code was replaced twice inside one span is one asset that
// changed. Naming it twice would have whoever reads this ask what is behind it
// twice.
func TestKeys_NameAnAssetThatChangedTwiceOnlyOnce(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getLogs": answered("[" + strings.Join([]string{
			logOf(asset, []string{upgraded, wordOf(stranger)}, "0x", tx),
			logOf(asset, []string{upgraded, wordOf(merchant)}, "0x", tx),
		}, ",") + "]"),
	})

	scan, err := evm.Keys(t.Context(), 0x10, 0x1f, []string{asset})

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{asset}; !slices.Equal(scan.Changed, want) {
		t.Errorf("the span read as %v changed, want %v", scan.Changed, want)
	}
}

// The filter is the provider's to apply, and what comes back is read as if it
// had not been: a log from a contract nobody asked about would put a key into
// the table that no asset of this deployment ever spent. A log announcing
// something else, or nothing, is not one of the two events this reads.
func TestKeys_ReadsNothingOutOfALogItDidNotAskFor(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getLogs": answered("[" + strings.Join([]string{
			used(fees, authorizer, spent, tx),
			logOf(fees, []string{upgraded, wordOf(stranger)}, "0x", tx),
			logOf(asset, []string{transferred, wordOf(authorizer), wordOf(merchant)}, "0x", tx),
			logOf(asset, nil, "0x", tx),
		}, ",") + "]"),
	})

	scan, err := evm.Keys(t.Context(), 0x10, 0x1f, []string{asset})

	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Consumed) != 0 || len(scan.Changed) != 0 {
		t.Errorf("the span read as %+v", scan)
	}
}

func TestKeys_RefusesALogThatCarriesNoKeyWhereOneBelongs(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"an authorisation with nothing indexed": "[" + logOf(asset, []string{authorizationUsed}, "0x", tx) + "]",
		"an authorisation without its key":      "[" + logOf(asset, []string{authorizationUsed, wordOf(authorizer)}, "0x", tx) + "]",
	} {
		t.Run(name, func(t *testing.T) {
			evm, _ := opening(t, map[string]string{"eth_getLogs": answered(answer)})

			if scan, err := evm.Keys(t.Context(), 0x10, 0x1f, []string{asset}); err == nil {
				t.Errorf("the span read as %+v out of %s", scan, answer)
			}
		})
	}
}

// A span nobody can read anything out of is not asked for. An endpoint asked
// for one either answers nothing, which would move the observer past blocks it
// never read, or refuses in words of its own.
func TestKeys_AsksForNothingItCouldNotReadAnAnswerTo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		first, last uint64
		assets      []string
		refused     bool
	}{
		{name: "a span that runs backwards", first: 0x1f, last: 0x10, assets: []string{asset}, refused: true},
		{name: "an asset that is not an address", first: 0x10, last: 0x1f, assets: []string{"jpyc"}, refused: true},
		{name: "no assets at all", first: 0x10, last: 0x1f, assets: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evm, provider := opening(t, map[string]string{})

			scan, err := evm.Keys(t.Context(), c.first, c.last, c.assets)

			if refused := err != nil; refused != c.refused {
				t.Errorf("Keys gave %+v, %v", scan, err)
			}
			if calls := provider.calls(); len(calls) != 0 {
				t.Errorf("the provider was asked %v", calls)
			}
		})
	}
}

// The topics are written out because a chain compares them as they are. What
// they are is the hash of the event's signature, and a digit mistyped in one
// would quietly stop matching the event it names.
func TestTopics_AreWhatTheHashOfEachEventSignatureComesTo(t *testing.T) {
	t.Parallel()
	for signature, topic := range map[string]string{
		"AuthorizationUsed(address,bytes32)": authorizationUsed,
		"Transfer(address,address,uint256)":  transferred,
		"Upgraded(address)":                  upgraded,
	} {
		if want := "0x" + hex.EncodeToString(keccak(signature)); topic != want {
			t.Errorf("%s is written as %s, and hashes to %s", signature, topic, want)
		}
	}
}

// keccak is the hash Ethereum names its events and its slots with, which is
// the one submitted to NIST and not the SHA-3 that came out of it.
func keccak(s string) []byte {
	sum := sha3.NewLegacyKeccak256()
	sum.Write([]byte(s))
	return sum.Sum(nil)
}
