package main

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
)

// awaiting is a deployment listing one asset on a simulated network, with a
// payment opened against it and a position on the network, which is what
// issuing a key needs.
func awaiting(t *testing.T, d deployment) *payment.Payment {
	t.Helper()
	return awaitingIn(t, d, jpyc(t))
}

// awaitingIn is that payment in an asset the caller names, for a document
// whose asset is not the one every other case here uses.
func awaitingIn(t *testing.T, d deployment, asset payment.Asset) *payment.Payment {
	t.Helper()
	amount, err := payment.ParseMoney(asset, "1000")
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: theAddress,
		ExpiresAt:   time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := payment.NewPostgres(d.pool.Conns()).Create(t.Context(), theAccount(t, d), p); err != nil {
		t.Fatal(err)
	}
	if err := observe.NewCursors(d.pool.Conns()).Init(t.Context(), p.Network(),
		observe.Position{Height: 1, Hash: "block1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_PaymentAwaitPrintsWhatAPayerSigns(t *testing.T) {
	d := accepting(t, "")
	p := awaiting(t, d)

	stdout, _, err := runArgs(t, "payment", "await", p.ID().String())

	if err != nil {
		t.Fatal(err)
	}
	printed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("line %q is not a name and a value", line)
		}
		printed[name] = value
	}
	live, _, ok, err := payment.NewPostgres(d.pool.Conns()).Live(t.Context(), theAccount(t, d), p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("no attempt was issued")
	}
	for name, want := range map[string]string{
		"contract":    "0x0000000000000000000000000000000000000001",
		"chainId":     "0",
		"to":          theAddress,
		"value":       "1000",
		"validAfter":  "0",
		"validBefore": strconv.FormatInt(live.ValidBefore().Unix(), 10),
		"nonce":       "0x" + live.Key(),
	} {
		if printed[name] != want {
			t.Errorf("%s = %q, want %q", name, printed[name], want)
		}
	}
	if len(printed) != 7 {
		t.Errorf("printed %d values, want the seven an authorisation carries:\n%s", len(printed), stdout)
	}

	back, _, err := payment.NewPostgres(d.pool.Conns()).Find(t.Context(), theAccount(t, d), p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want %s", back.Status(), payment.AwaitingPayment)
	}
}

func TestRun_PaymentAwaitRefusesANetworkNothingIsReading(t *testing.T) {
	d := accepting(t, "")
	amount, err := payment.ParseMoney(jpyc(t), "1000")
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: theAddress,
		ExpiresAt:   time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := payment.NewPostgres(d.pool.Conns()).Create(t.Context(), theAccount(t, d), p); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "payment", "await", p.ID().String())

	if err == nil {
		t.Fatalf("a key was issued on a network nobody reads:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), string(p.Network())) {
		t.Errorf("the refusal does not name the network: %v", err)
	}
}

// A run that stopped before the key was issued leaves the payment payable.
// The operator fixes what stopped it and runs the command again, and this is
// what says the second run gets a key rather than a refusal.
func TestRun_PaymentAwaitCanBeRunAgainAfterItStopped(t *testing.T) {
	d := accepting(t, "")
	amount, err := payment.ParseMoney(jpyc(t), "1000")
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: theAddress,
		ExpiresAt:   time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := payment.NewPostgres(d.pool.Conns()).Create(t.Context(), theAccount(t, d), p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runArgs(t, "payment", "await", p.ID().String()); err == nil {
		t.Fatal("a key was issued on a network nobody reads")
	}

	if err := observe.NewCursors(d.pool.Conns()).Init(t.Context(), p.Network(),
		observe.Position{Height: 1, Hash: "block1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runArgs(t, "payment", "await", p.ID().String())

	if err != nil {
		t.Fatalf("the second run was refused: %v", err)
	}
	if !strings.Contains(stdout, "nonce 0x") {
		t.Errorf("the second run printed no nonce:\n%s", stdout)
	}
}

func TestRun_HelpSaysWhatPaymentAwaitIsFor(t *testing.T) {
	t.Parallel()
	stdout, _, err := runArgs(t, "help")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "suco Checkout") {
		t.Errorf("the usage text does not say what payment await stands in for:\n%s", stdout)
	}
}

// The chain a payer's signature is bound to is the one the document names.
// A network that runs inside the process has none, and prints zero.
func TestRun_PaymentAwaitPrintsTheChainTheDocumentNames(t *testing.T) {
	d := deployed(t)
	document(t, evmDocumentOnChainOne(t))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}
	p := awaiting(t, d)

	stdout, _, err := runArgs(t, "payment", "await", p.ID().String())

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "chainId 1\n") {
		t.Errorf("the authorisation names no chain the document declared:\n%s", stdout)
	}
}

// What a payer signs names the contract, and a signature binds to the bytes it
// was shown. It is written the way the chain writes one, so that what the
// payer authorises is what a transfer will be compared against.
//
// The destination comes from the row asset accept wrote, which is written the
// same way and checked where that command is.
func TestRun_PaymentAwaitPrintsTheContractTheChainCompares(t *testing.T) {
	d := deployed(t)
	document(t, evmDocumentOnChainOne(t))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theChecksummed); err != nil {
		t.Fatal(err)
	}
	asset, err := payment.NewAsset("local", strings.ToLower(theChecksummed), "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	p := awaitingIn(t, d, asset)

	stdout, _, err := runArgs(t, "payment", "await", p.ID().String())

	if err != nil {
		t.Fatal(err)
	}
	if want := "contract " + strings.ToLower(theChecksummed); !strings.Contains(stdout, want+"\n") {
		t.Errorf("what a payer signs has no line %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, theChecksummed) {
		t.Errorf("what a payer signs holds the form the operator typed:\n%s", stdout)
	}
}

// evmDocumentOnChainOne is a document with jpyc on an evm network read through a
// provider that answers what the contract signs under, which registering
// the asset asks for.
func evmDocumentOnChainOne(t *testing.T) string {
	t.Helper()
	onChain, err := evm.Domain("JPY Coin", "1", 1, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := answering(t, map[string]any{"eth_call": "0x" + hex.EncodeToString(onChain[:]), "eth_call 0x8e204c43": clear})
	return evmDocument(endpoint, 1, "    eip712:\n      name: JPY Coin\n      version: \"1\"\n")
}
