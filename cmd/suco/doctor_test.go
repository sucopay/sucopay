package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
)

func TestDescribeVersion_BoundsAndQuotesWhatTheServerSent(t *testing.T) {
	t.Parallel()
	// The report is laid out in columns and reaches a terminal. What arrives
	// in this field was chosen at the other end of a network, by whoever is
	// answering, which is not always the database the operator meant.
	for _, c := range []struct {
		name    string
		version string
	}{
		{"a row of its own", "17.11\n  database.url\tnot set\tdefault"},
		{"an escape sequence", "17.11\x1b[31m all clear\x1b[0m"},
		{"a column separator", "17.11\tsomething else"},
		{"more than a line", strings.Repeat("17.11 ", 200)},
		{"multi-byte, cut mid-rune", strings.Repeat("バージョン", 100)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := describeVersion(c.version)

			if strings.ContainsAny(got, "\n\t\x1b") {
				t.Errorf("reaches the report unquoted: %q", got)
			}
			if len(got) > maxDescription*4 {
				t.Errorf("length %d, want something a report can hold: %q", len(got), got)
			}
		})
	}
}

func TestDescribeVersion_LeavesAnOrdinaryVersionAlone(t *testing.T) {
	t.Parallel()
	if got := describeVersion("17.11"); got != "PostgreSQL 17.11" {
		t.Errorf("describeVersion(17.11) = %q, want it unchanged", got)
	}
}

// answering is a JSON-RPC endpoint that gives the answers named, by method,
// and refuses anything else. It stands in for a provider, so that a document
// can name an endpoint a test controls.
func answering(t *testing.T, answers map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			t.Errorf("the provider was asked something that is not JSON-RPC: %v", err)
			return
		}
		w.Header().Set("content-type", "application/json")
		// A call to a contract is told apart by what it calls: the whole
		// data, then the selector alone, then the method as every call is.
		answer, known := any(nil), false
		if len(asked.Params) > 0 {
			if call, ok := asked.Params[0].(map[string]any); ok {
				if data, ok := call["data"].(string); ok {
					if answer, known = answers[asked.Method+" "+data]; !known && len(data) >= 10 {
						answer, known = answers[asked.Method+" "+data[:10]]
					}
				}
			}
		}
		if !known {
			answer, known = answers[asked.Method]
		}
		if !known {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32601, "message": "the method " + asked.Method + " is not here"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": answer})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// block is a header as a provider answers with one, at the height given.
// Nothing here reads a parent, so every block names the same one.
func block(height string) map[string]any {
	return map[string]any{
		"number": height, "hash": "0x" + strings.Repeat("11", 32),
		"parentHash": "0x" + strings.Repeat("22", 32), "timestamp": "0x6800000",
	}
}

// networksIn is the part of a report that is about the networks, so that a
// word the settings above it also hold is not read as this having said it.
func networksIn(t *testing.T, report string) string {
	t.Helper()
	_, networks, found := strings.Cut(report, "\nnetworks:\n")
	if !found {
		t.Fatalf("the report says nothing about the networks:\n%s", report)
	}
	return networks
}

// A report says what each network is and where it stands, so that an operator
// diagnosing a deployment that sees no payments learns which end is quiet.
//
// Read through a kind whose name is not what the chain answers with, so that a
// column printing the wrong one of the two is caught. A chain inside the
// process answers with its own kind, and the two would read alike.
func TestRun_DoctorSaysWhereEachNetworkStands(t *testing.T) {
	deployed(t)
	endpoint := answering(t, map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
	})
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: %s\n%s", namingADatabase(), endpoint, anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	networks := networksIn(t, stdout)
	for _, want := range []string{
		"local",     // the name the document gives it
		"evm",       // what this build opens it as
		"chain 137", // what the chain calls itself
		"latest 16", // where the chain stands
		"final 16",  //
		"no cursor", // how far this deployment has read it
		"jpyc",      // what settles on it
		// and the code that chain runs for it, which is what an upgrade of a
		// proxy changes under a deployment that is not watching for it.
		"implementation 0x" + strings.Repeat("00", 20),
		// and that the contract was not asked whether it is paused, since this
		// provider answers no call to it: silence is not "not paused".
		"paused not read",
	} {
		if !strings.Contains(networks, want) {
			t.Errorf("the report does not say %q of the network:\n%s", want, networks)
		}
	}
}

// A network settled through others is one whose spare an operator cannot see
// from the settling: two answering settle as well as four do, until the next
// one goes down.
func TestRun_DoctorSaysHowManyOthersAnswer(t *testing.T) {
	answers := map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
	}
	const dead = "http://127.0.0.1:1/"
	for what, tt := range map[string]struct {
		up   int
		down int
		want string
	}{
		"a spare":     {3, 0, "3 of 3 others answer, 0 waiting"},
		"no spare":    {2, 1, "2 of 3 others answer, no spare"},
		"too few":     {1, 1, "1 of 2 others answer, too few to settle"},
		"none at all": {0, 2, "0 of 2 others answer, too few to settle"},
	} {
		t.Run(what, func(t *testing.T) {
			deployed(t)
			others := ""
			for range tt.up {
				others += "        - " + answering(t, answers) + "\n"
			}
			for range tt.down {
				others += "        - " + dead + "\n"
			}
			document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
				"    rpc:\n      others:\n%s%s", namingADatabase(), others, anAsset("local")))

			stdout, _, err := runArgs(t, "doctor")

			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if networks := networksIn(t, stdout); !strings.Contains(networks, tt.want) {
				t.Errorf("the report does not say %q of the others:\n%s", tt.want, networks)
			}
		})
	}
}

// A network with a node of the operator's own settles through it alone, and
// has no others to count.
func TestRun_DoctorCountsNoOthersForANetworkWithAnOwnNode(t *testing.T) {
	deployed(t)
	endpoint := answering(t, map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
	})
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: %s\n      others:\n        - %s\n%s", namingADatabase(), endpoint, endpoint, anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if networks := networksIn(t, stdout); strings.Contains(networks, "others answer") {
		t.Errorf("the report counts others for a network with an own node:\n%s", networks)
	}
}

// A transfer the endpoints disagree about is one nothing settles and nothing
// resolves, so the count is in the report from the first round it happens.
func TestRun_DoctorCountsWhatTheEndpointsDisagreeAbout(t *testing.T) {
	d := accepting(t, "")
	p := awaiting(t, d)
	if _, _, err := runArgs(t, "payment", "await", p.ID().String()); err != nil {
		t.Fatal(err)
	}
	var attempt, key string
	if err := d.pool.Conns().QueryRow(t.Context(), `select id, key from attempts`).Scan(&attempt, &key); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Conns().Exec(t.Context(), `
		insert into observations (account_id, payment_id, attempt_id, network, key, tx, position,
			block_height, block_hash, block_time, asset, authorizer, sender, recipient, value,
			reason, implementation, seen_at, final_at, disagreed_at)
		select account_id, payment_id, id, $1, key, 'tx1', 0, 1, 'block1', now(), 'asset', 'a', 'a', 'b', 1000,
			'matched', 'implementation', now(), now(), now()
		  from attempts where id = $2`, string(p.Network()), attempt); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if networks := networksIn(t, stdout); !strings.Contains(networks, "1 waiting to settle, 1 disagreed about") {
		t.Errorf("the report does not count what the endpoints disagree about:\n%s", networks)
	}
}

// A document copied from the step before production, with the network's name
// changed and nothing else, points at a testnet. The report says so, and says
// nothing of the kind for the chain money is on.
func TestRun_DoctorNamesATestnet(t *testing.T) {
	cases := map[string]struct {
		id   string
		want string
	}{
		"Polygon": {"137", "chain 137, latest"},
		// By the number and not by the table, so that a wrong number in
		// the table is a failure here and not a report agreeing with itself.
		"Polygon Amoy by its number": {"80002", "chain 80002 (Polygon Amoy, a testnet)"},
	}
	for id, name := range testnets {
		cases[name] = struct {
			id   string
			want string
		}{id, fmt.Sprintf("chain %s (%s, a testnet)", id, name)}
	}
	for what, tt := range cases {
		t.Run(what, func(t *testing.T) {
			deployed(t)
			id, err := strconv.ParseUint(tt.id, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := answering(t, map[string]any{
				"eth_chainId":          fmt.Sprintf("0x%x", id),
				"eth_getBlockByNumber": block("0x10"),
				"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
			})
			document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: %s\n"+
				"    rpc:\n      own: %s\n%s", namingADatabase(), tt.id, endpoint, anAsset("local")))

			stdout, _, err := runArgs(t, "doctor")

			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if networks := networksIn(t, stdout); !strings.Contains(networks, tt.want) {
				t.Errorf("the report does not say %q:\n%s", tt.want, networks)
			}
		})
	}
}

// The chain answering and the cursor being readable are two questions with
// two answers. Asked as one, an operator whose database is short of the table
// reads it as an endpoint that will not answer, and changes their provider.
func TestRun_DoctorTellsAChainItCannotReadFromACursorItCannot(t *testing.T) {
	d := deployed(t)
	document(t, namingADatabase()+anAssetOn("local", ""))
	if _, err := d.pool.Conns().Exec(t.Context(), `drop table observation_cursors`); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	networks := networksIn(t, stdout)
	if !strings.Contains(networks, "the cursor could not be read") {
		t.Errorf("the report does not say it was the cursor that could not be read:\n%s", networks)
	}
	// The chain answered, and the report has to say so: it is the half the
	// operator would otherwise go and change.
	if !strings.Contains(networks, "latest") {
		t.Errorf("the report does not say the chain answered:\n%s", networks)
	}
}

// A network the document names and nothing can reach is the first thing an
// operator wants named, with what the provider said about it.
func TestRun_DoctorSaysWhyANetworkCouldNotBeRead(t *testing.T) {
	deployed(t)
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: http://127.0.0.1:1/v1?key=secret\n%s", namingADatabase(), anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "local") {
		t.Errorf("the report does not name the network:\n%s", stdout)
	}
	// The endpoint is a secret, and a report is what somebody pastes into a
	// public issue.
	for _, part := range []string{"key=secret", "secret", "127.0.0.1:1", "/v1"} {
		if strings.Contains(stdout, part) {
			t.Errorf("the report holds %q of the endpoint:\n%s", part, stdout)
		}
	}
}

// A chain that is not the one the document names is a deployment reading
// somebody else's blocks, and every payment on it would go unseen.
func TestRun_DoctorSaysWhenTheChainIsNotTheOneNamed(t *testing.T) {
	deployed(t)
	endpoint := answering(t, map[string]any{
		"eth_chainId":          "0x1",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
	})
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: %s\n%s", namingADatabase(), endpoint, anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "chain-mismatch") {
		t.Errorf("the report does not say the chain is another one:\n%s", stdout)
	}
	// Both numbers, so that an operator can tell which end to change.
	for _, want := range []string{"137", "1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not name %s:\n%s", want, stdout)
		}
	}
}

// A deployment with no database has nowhere to have written how far it read,
// so the report says what each network is and stops there.
func TestRun_DoctorNamesTheKindOfANetworkWithNoDatabase(t *testing.T) {
	document(t, anAssetOn("local", ""))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "simulated") {
		t.Errorf("the report does not say what the network is:\n%s", stdout)
	}
}

// A document is read into the form the chain compares, so that a reference
// written the way a block explorer shows one is the same asset as the one a
// transfer names.
func TestRun_DoctorShowsAReferenceTheWayTheChainWritesIt(t *testing.T) {
	document(t, anEVMAssetOn("local"))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	line := reportLine(t, stdout, "assets.jpyc.reference")
	if !strings.Contains(line, strings.ToLower(theChecksummed)) {
		t.Errorf("assets.jpyc.reference reads %q, want it in lower case", line)
	}
}

// A report says how much is waiting for the endpoints to be asked about it.
// The worker is in the process that serves, so what a report can say about the
// settling is what the database holds for it.
func TestRun_DoctorSaysWhatIsWaitingToSettle(t *testing.T) {
	deployed(t)
	endpoint := answering(t, map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
	})
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: %s\n%s", namingADatabase(), endpoint, anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if networks := networksIn(t, stdout); !strings.Contains(networks, "0 waiting to settle") {
		t.Errorf("the report does not say what is waiting to settle:\n%s", networks)
	}
}

// What the deployment's deliveries have come to is counted from the
// database, by account, with the endpoints the failed ones were to and the
// endpoints whose secret the deployment can no longer read.
func TestRun_DoctorSaysWhatIsPendingAndFailedToDeliverAndWhatItCannotSign(t *testing.T) {
	d := deployed(t)
	pool := d.pool.Conns()
	const account = "00000000-0000-0000-0000-000000000001"
	const endpoint = "e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1"
	const paymentID = "11111111111111111111111111111111"
	for _, sql := range []string{
		`insert into webhook_endpoints (id, scope, account_id, url, description, enabled, secret, key_id, created_at)
		 values ('` + endpoint + `', 'account', '` + account + `', 'https://hooks.example/in', '', true, '\x00', 'oldkey', now())`,
		`insert into payments (id, account_id, asset_network, asset_reference, asset_symbol, asset_decimals,
		                       amount, received, destination, status, created_at, expires_at)
		 values ('` + paymentID + `', '` + account + `', 'polygon', 'r', 'JPYC', 18, 1, 1, '0xabc', 'succeeded', now(), now() + interval '1 hour')`,
		`insert into webhook_deliveries (id, endpoint_id, account_id, payment_id, type, occurred_at, payload, state, next_at, created_at)
		 values ('d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1', '` + endpoint + `', '` + account + `', '` + paymentID + `', 'payment.succeeded', now(), '{}', 'pending', now(), now()),
		        ('d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2', '` + endpoint + `', '` + account + `', '` + paymentID + `', 'payment.succeeded', now(), '{}', 'failed', null, now())`,
	} {
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	for _, want := range []string{
		"webhooks:\n",
		"  " + account + "  1 pending, 1 failed to " + endpoint + "\n",
		"  1 endpoints hold a secret sealed under another key",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, stdout)
		}
	}
}

func TestRun_DoctorSaysNothingIsPendingOrFailedWhereNothingIs(t *testing.T) {
	deployed(t)

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "webhooks: nothing pending, nothing failed\n") {
		t.Errorf("the report does not say the deliveries stand clear:\n%s", stdout)
	}
}

// An issuer stopping a token is a fact about the token the report says of
// each asset, and a contract that will not say is said to have not said:
// nothing here reads silence as not paused.
func TestRun_DoctorSaysWhichAssetsTheIssuerHasPaused(t *testing.T) {
	deployed(t)
	endpoint := answering(t, map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
		"eth_call 0x5c975abb":  set,
	})
	document(t, fmt.Sprintf("%snetworks:\n  local:\n    kind: evm\n    chain_id: 137\n"+
		"    rpc:\n      own: %s\n%s", namingADatabase(), endpoint, anAsset("local")))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	networks := networksIn(t, stdout)
	if !strings.Contains(networks, ", paused") || strings.Contains(networks, "paused not read") {
		t.Errorf("the report does not say the asset is paused:\n%s", networks)
	}
}

// Where the one account is paid is read off the contract with the rest of the
// report, and an address the contract refuses is said so. Two accounts have
// no one address to ask about, and the report says nothing rather than
// choosing one.
func TestRun_DoctorSaysWhenTheAcceptedAddressIsBlocklisted(t *testing.T) {
	d := deployed(t)
	onChain, err := evm.Domain("JPY Coin", "1", 137, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"
	clearing := answering(t, map[string]any{
		"eth_call": "0x" + hex.EncodeToString(onChain[:]), "eth_call 0x8e204c43": clear,
	})
	document(t, evmDocument(clearing, 137, given))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}
	// The issuer then blocklists the address.
	listing := answering(t, map[string]any{
		"eth_chainId":          "0x89",
		"eth_getBlockByNumber": block("0x10"),
		"eth_getStorageAt":     "0x" + strings.Repeat("00", 32),
		"eth_call 0x5c975abb":  clear,
		"eth_call 0x8e204c43":  set,
	})
	document(t, evmDocument(listing, 137, given))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if networks := networksIn(t, stdout); !strings.Contains(networks, "paid to "+theAddress+", which is blocklisted") {
		t.Errorf("the report does not say the address is blocklisted:\n%s", networks)
	}

	if _, err := d.pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ('00000000-0000-0000-0000-000000000002', 'second')`); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runArgs(t, "doctor")
	if err != nil {
		t.Fatalf("with two accounts, err = %v, want none", err)
	}
	if networks := networksIn(t, stdout); strings.Contains(networks, "blocklisted") {
		t.Errorf("with two accounts, the report chose one to say whose address is blocklisted:\n%s", networks)
	}
}

// A network that cannot be read says so on its own line, and its assets say
// nothing more: not whether they are paused, and not whether the address they
// are paid to is refused. The second would be asked of the same connection,
// once per asset, and a provider that times out would make the report wait
// that many times over to say the same thing.
func TestRun_DoctorAsksNothingMoreOfANetworkItCouldNotRead(t *testing.T) {
	d := deployed(t)
	onChain, err := evm.Domain("JPY Coin", "1", 137, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	given := "    eip712:\n      name: JPY Coin\n      version: \"1\"\n"
	document(t, evmDocument(answering(t, map[string]any{
		"eth_call": "0x" + hex.EncodeToString(onChain[:]), "eth_call 0x8e204c43": clear,
	}), 137, given))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}
	if n := rowsAccepted(t, d); n != 1 {
		t.Fatalf("%d addresses accepted, want one", n)
	}
	// A provider that answers no call at all: the network cannot be read.
	document(t, evmDocument(answering(t, map[string]any{}), 137, given))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	networks := networksIn(t, stdout)
	if !strings.Contains(networks, "not read") {
		t.Errorf("the report does not say the network could not be read:\n%s", networks)
	}
	for _, more := range []string{"paused", "refuses transfers", "blocklisted"} {
		if strings.Contains(networks, more) {
			t.Errorf("the report says %q of an asset on a network it could not read:\n%s", more, networks)
		}
	}
}
