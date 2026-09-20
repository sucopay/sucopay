package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/accepted"
	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/payment"
)

// theAddress is the address these tests have a payment paid to. Its shape is not
// checked here: an adapter decides what an address on its network looks like.
const theAddress = "0x00000000000000000000000000000000000000aa"

// What a contract answers for a flag that is not set, and for one that is.
const (
	clear = "0x0000000000000000000000000000000000000000000000000000000000000000"
	set   = "0x0000000000000000000000000000000000000000000000000000000000000001"
)

// accepting is a deployment whose document lists jpyc on a simulated network,
// and after it whatever more is written into the assets section.
func accepting(t *testing.T, more string) deployment {
	t.Helper()
	d := deployed(t)
	document(t, namingADatabase()+anAssetOn("local", "")+more)
	return d
}

// jpyc is the asset the documents of these tests list under that name.
func jpyc(t *testing.T) payment.Asset {
	t.Helper()
	asset, err := payment.NewAsset("local", "0x0000000000000000000000000000000000000001", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

// theAccount is the one account a deployment has.
func theAccount(t *testing.T, d deployment) payment.AccountID {
	t.Helper()
	accounts, err := d.store.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("the database has %d accounts, want one", len(accounts))
	}
	return payment.AccountID(accounts[0])
}

// rowsAccepted counts what has been accepted, under any account. Read from
// the table rather than through the store, so that a command writing a row
// where none is wanted is seen whichever account it wrote it under.
func rowsAccepted(t *testing.T, d deployment) int {
	t.Helper()
	var n int
	if err := d.pool.Conns().QueryRow(t.Context(), `select count(*) from accepted_assets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRun_AssetAcceptRecordsTheAddressItPrints(t *testing.T) {
	d := accepting(t, "")

	stdout, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress)

	if err != nil {
		t.Fatal(err)
	}
	if want := "Accepted JPYC on local, paying to " + theAddress + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	destination, ok, err := accepted.NewPostgres(d.pool.Conns()).Destination(t.Context(), theAccount(t, d), jpyc(t))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(destination) != theAddress {
		t.Errorf("Destination = %q, %v, want %q, true", destination, ok, theAddress)
	}
}

func TestRun_AssetListShowsEveryAssetOfTheDocument(t *testing.T) {
	accepting(t, "  usdc:\n    network: local\n"+
		"    reference: \"0x0000000000000000000000000000000000000002\"\n"+
		"    symbol: USDC\n    decimals: 6\n")
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "asset", "list")

	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"NAME", "ASSET", "PAID", "TO"},
		{"jpyc", "JPYC", "on", "local", theAddress},
		{"usdc", "USDC", "on", "local", "not", "accepted"},
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("stdout has %d lines, want %d:\n%s", len(lines), len(want), stdout)
	}
	for i, line := range lines {
		if got := strings.Fields(line); !slices.Equal(got, want[i]) {
			t.Errorf("line %d = %q, want the words %q", i, line, want[i])
		}
	}
}

func TestRun_AssetListWithNoAssetInTheDocumentShowsTheHeaderAlone(t *testing.T) {
	deployed(t)

	stdout, _, err := runArgs(t, "asset", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := strings.Fields(stdout); !slices.Equal(got, []string{"NAME", "ASSET", "PAID", "TO"}) {
		t.Errorf("stdout = %q, want the header and nothing under it", stdout)
	}
}

// TestRun_AssetListFollowsAnAssetTheDocumentRenamed is the claim that an
// asset is accepted by what identifies it and not by its name: the name is
// one deployment's to change, and changing it does not send a payment of the
// token anywhere else.
func TestRun_AssetListFollowsAnAssetTheDocumentRenamed(t *testing.T) {
	accepting(t, "")
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}
	document(t, namingADatabase()+strings.Replace(anAssetOn("local", ""), "  jpyc:", "  yen:", 1))

	stdout, _, err := runArgs(t, "asset", "list")

	if err != nil {
		t.Fatal(err)
	}
	want := []string{"yen", "JPYC", "on", "local", theAddress}
	_, row, _ := strings.Cut(stdout, "\n")
	if got := strings.Fields(row); !slices.Equal(got, want) {
		t.Errorf("row = %q, want the words %q", row, want)
	}
}

func TestRun_AssetAcceptRefusesANameTheDocumentDoesNotListWithoutRepeatingIt(t *testing.T) {
	d := accepting(t, "")

	stdout, stderr, err := runArgs(t, "asset", "accept", "usdc", theAddress)

	if err == nil {
		t.Fatal("want an error, got none")
	}
	for name, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
		if strings.Contains(text, "usdc") {
			t.Errorf("%s repeats the name: %q", name, text)
		}
	}
	if !strings.Contains(err.Error(), os.Getenv("SUCO_CONFIG")) {
		t.Errorf("error does not name the document that lists the names: %v", err)
	}
	if !strings.Contains(err.Error(), "suco asset list") {
		t.Errorf("error does not name the command that shows the names: %v", err)
	}
	if n := rowsAccepted(t, d); n != 0 {
		t.Errorf("%d rows were written, want none", n)
	}
}

func TestRun_AssetAcceptRefusesAnAddressAPaymentCouldNotBePaidTo(t *testing.T) {
	d := accepting(t, "")
	for _, c := range []struct{ name, address string }{
		{"none", ""},
		{"a character that does not show up", "0x00\u200b01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := runArgs(t, "asset", "accept", "jpyc", c.address)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.HasPrefix(err.Error(), "address:") {
				t.Errorf("error is not the one ParseAddress gives: %v", err)
			}
		})
	}
	if n := rowsAccepted(t, d); n != 0 {
		t.Errorf("%d rows were written, want none", n)
	}
}

func TestRun_AssetCommandsRefuseToChooseAmongTwoAccountsAsCredentialNewDoes(t *testing.T) {
	d := accepting(t, "")
	if _, err := d.pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ('00000000-0000-0000-0000-000000000002', 'second')`); err != nil {
		t.Fatal(err)
	}
	_, _, byNew := runArgs(t, "credential", "new", "--read-only")
	if byNew == nil {
		t.Fatal("credential new chose an account, want a refusal")
	}
	for _, args := range [][]string{{"asset", "accept", "jpyc", theAddress}, {"asset", "list"}} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			_, _, err := runArgs(t, args...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if err.Error() != byNew.Error() {
				t.Errorf("refuses with:\n  %v\ncredential new with:\n  %v", err, byNew)
			}
		})
	}
	if n := rowsAccepted(t, d); n != 0 {
		t.Errorf("%d rows were written, want none", n)
	}
}

// The address an operator types is compared with what a chain writes, and a
// block explorer hands them the mixed-case form to copy.
func TestRun_AssetAcceptStoresAnAddressTheWayTheChainWritesIt(t *testing.T) {
	deployed(t)
	// Registration asks the contract what it signs under, so the provider
	// answers that.
	onChain, err := evm.Domain("JPY Coin", "1", 137, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := answering(t, map[string]any{"eth_call": "0x" + hex.EncodeToString(onChain[:]), "eth_call 0x8e204c43": clear})
	document(t, evmDocument(endpoint, 137, "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"))

	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theChecksummed); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runArgs(t, "asset", "list")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, strings.ToLower(theChecksummed)) {
		t.Errorf("the list does not show the address in lower case:\n%s", stdout)
	}
	if strings.Contains(stdout, theChecksummed) {
		t.Errorf("the list shows the address as it was typed:\n%s", stdout)
	}
}

// An address that is not one on the asset's chain is refused before anything
// is paid to it.
func TestRun_AssetAcceptRefusesAnAddressThatIsNotOneOnItsChain(t *testing.T) {
	deployed(t)
	document(t, namingADatabase()+anEVMAssetOn("local"))

	_, _, err := runArgs(t, "asset", "accept", "jpyc", "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD")

	if err == nil {
		t.Fatal("an address whose checksum does not hold was accepted")
	}
}

// evmDocument is a document naming a database and one evm network read
// through a provider at endpoint, with jpyc on it and the domain given.
func evmDocument(endpoint string, chainID uint64, domain string) string {
	return namingADatabase() + "networks:\n  local:\n    kind: evm\n    chain_id: " + strconv.FormatUint(chainID, 10) +
		"\n    rpc:\n      own: " + endpoint + "\n" + anAsset("local") + domain
}

// What a payer's wallet signs under has to be what the contract signs
// under. The contract is asked at registration, and a document whose
// domain is not the contract's is refused there rather than at the first
// payment.
func TestRun_AssetAcceptChecksTheDomainAgainstTheContract(t *testing.T) {
	d := deployed(t)
	onChain, err := evm.Domain("JPY Coin", "1", 137, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := answering(t, map[string]any{"eth_call": "0x" + hex.EncodeToString(onChain[:]), "eth_call 0x8e204c43": clear})
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"

	document(t, evmDocument(endpoint, 137, given))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Errorf("with the contract's domain: %v, want accepted", err)
	}
	document(t, evmDocument(endpoint, 137, "    eip712:\n      name: JPY Coin\n      version: \"2\"\n"))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err == nil || !strings.Contains(err.Error(), "eip712") {
		t.Errorf("with another version: %v, want a refusal naming eip712", err)
	}
	document(t, evmDocument(endpoint, 137, "    eip712:\n      name: JPYC\n      version: \"1\"\n"))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err == nil || !strings.Contains(err.Error(), "eip712") {
		t.Errorf("with another name: %v, want a refusal naming eip712", err)
	}
	document(t, evmDocument(endpoint, 137, ""))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err == nil || !strings.Contains(err.Error(), "eip712") {
		t.Errorf("with no domain given: %v, want a refusal naming eip712", err)
	}
	if _, ok, err := accepted.NewPostgres(d.pool.Conns()).Destination(t.Context(), theAccount(t, d), jpyc(t)); err != nil || !ok {
		t.Errorf("the first, accepted registration is gone: %t, %v", ok, err)
	}
}

// An address the contract refuses transfers to is one nothing paid would
// arrive at, and it is refused at registration rather than at the first
// payment that goes nowhere. A contract that will not say is refused too: the
// answer is the contract's to give, and an address is not registered on the
// strength of a provider that would not carry it.
func TestRun_AssetAcceptRefusesAnAddressTheContractRefusesTransfersTo(t *testing.T) {
	onChain, err := evm.Domain("JPY Coin", "1", 137, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"
	for name, c := range map[string]struct {
		answer any
		want   string
	}{
		"one the contract refuses":                    {set, "refuses transfers to " + theAddress},
		"one the contract will not say about":         {"0x", "answered nothing"},
		"one the provider answers something else for": {nil, "asking whether"},
	} {
		t.Run(name, func(t *testing.T) {
			d := deployed(t)
			answers := map[string]any{"eth_call": "0x" + hex.EncodeToString(onChain[:])}
			if c.answer != nil {
				answers["eth_call 0x8e204c43"] = c.answer
			} else {
				// Not a word at all, which is what a provider that has lost the
				// shape of the call answers with.
				answers["eth_call 0x8e204c43"] = map[string]any{"not": "a word"}
			}
			document(t, evmDocument(answering(t, answers), 137, given))

			_, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress)

			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want a refusal saying %q", err, c.want)
			}
			if n := rowsAccepted(t, d); n != 0 {
				t.Errorf("%d addresses were accepted, want none", n)
			}
		})
	}
}

// abiString is a string as a contract answers one: where its bytes begin,
// how many there are, and the bytes padded to a whole word.
func abiString(s string) string {
	h := hex.EncodeToString([]byte(s))
	return fmt.Sprintf("0x%064x%064x%s%s", 32, len(s), h, strings.Repeat("0", (64-len(h)%64)%64))
}

// A contract that answers no separator, which JPYC is, is checked by what
// it does answer: the chain against the network's chain_id and its name
// against the domain's. The version is then the document's word, and the
// line that confirms the registration says so.
func TestRun_AssetAcceptChecksTheChainAndTheNameWhereTheContractAnswersNoSeparator(t *testing.T) {
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"
	answers := func(name string, chain any) map[string]any {
		a := map[string]any{
			"eth_call 0x3644e515": refusal{3, "execution reverted", nil},
			"eth_call 0x06fdde03": abiString(name),
			"eth_call 0x8e204c43": clear,
		}
		if chain != nil {
			a["eth_chainId"] = chain
		}
		return a
	}

	d := deployed(t)
	document(t, evmDocument(answering(t, answers("JPY Coin", "0x89")), 137, given))
	stdout, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress)
	if err != nil {
		t.Fatalf("with the contract's chain and name: %v, want accepted", err)
	}
	if want := "Accepted JPYC on local, paying to " + theAddress + `, taking eip712.version "1" from `; !strings.HasPrefix(stdout, want) {
		t.Errorf("stdout = %q, want it to begin %q", stdout, want)
	}
	if n := rowsAccepted(t, d); n != 1 {
		t.Errorf("accepted %d, want the one", n)
	}

	for name, c := range map[string]struct {
		answers map[string]any
		chainID uint64
		want    string
	}{
		"another name":                {answers("JPYC", "0x89"), 137, "calls itself"},
		"another chain":               {answers("JPY Coin", "0x89"), 1, "chain_id"},
		"a provider not saying which": {answers("JPY Coin", nil), 137, "which chain"},
	} {
		t.Run(name, func(t *testing.T) {
			d := deployed(t)
			document(t, evmDocument(answering(t, c.answers), c.chainID, given))

			_, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress)

			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want a refusal saying %q", err, c.want)
			}
			if n := rowsAccepted(t, d); n != 0 {
				t.Errorf("accepted %d, want none", n)
			}
		})
	}
}

// A contract answering no separator is one thing, and every other way of not
// reading one is another: a revert with a reason is a function that ran, an
// empty answer is an address with no code, and a provider's own refusal is
// neither. None of them has the name checked instead.
func TestRun_AssetAcceptReadsNothingElseAsTheContractAnsweringNoSeparator(t *testing.T) {
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"
	for name, c := range map[string]struct {
		separator any
		want      string
	}{
		"a revert with a reason":     {refusal{3, "execution reverted", "0x08c379a0"}, "signs under"},
		"an empty answer":            {"0x", "no DOMAIN_SEPARATOR()"},
		"the provider's own refusal": {refusal{-32601, "no such method", nil}, "signs under"},
	} {
		t.Run(name, func(t *testing.T) {
			d := deployed(t)
			document(t, evmDocument(answering(t, map[string]any{
				"eth_chainId":         "0x89",
				"eth_call 0x3644e515": c.separator,
				"eth_call 0x06fdde03": abiString("JPY Coin"),
				"eth_call 0x8e204c43": clear,
			}), 137, given))

			_, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress)

			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want a refusal saying %q", err, c.want)
			}
			if n := rowsAccepted(t, d); n != 0 {
				t.Errorf("accepted %d, want none", n)
			}
		})
	}
}
