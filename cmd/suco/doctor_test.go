package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			t.Errorf("the provider was asked something that is not JSON-RPC: %v", err)
			return
		}
		w.Header().Set("content-type", "application/json")
		answer, known := answers[asked.Method]
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
	} {
		if !strings.Contains(networks, want) {
			t.Errorf("the report does not say %q of the network:\n%s", want, networks)
		}
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
