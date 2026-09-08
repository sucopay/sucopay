package evm

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// request is one call a provider was asked to answer.
type request struct {
	method string
	params []any
}

// provider is a chain a test writes the answers of. It replies to each method
// with the body beside it and remembers what it was asked; a method nobody
// gave an answer to fails the test, so an adapter that calls what it should
// not is caught here rather than reading something plausible.
//
// A body is found under the method, or under the method and its first
// parameter, which is how one call is told from another where the same method
// is asked more than once.
type provider struct {
	t      *testing.T
	mu     sync.Mutex
	bodies map[string]string
	asked  []request
}

// opening is an adapter reading a provider that answers those bodies.
func opening(t *testing.T, bodies map[string]string) (*network, *provider) {
	t.Helper()
	p := &provider{t: t, bodies: bodies}
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	return &network{client: opened(t, server.URL)}, p
}

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		p.t.Error(err)
	}
	p.mu.Lock()
	p.asked = append(p.asked, request{method: body.Method, params: body.Params})
	answer, found := "", false
	if len(body.Params) > 0 {
		if first, ok := body.Params[0].(string); ok {
			answer, found = p.bodies[body.Method+" "+first]
		}
	}
	if !found {
		answer, found = p.bodies[body.Method]
	}
	p.mu.Unlock()
	if !found {
		p.t.Errorf("the adapter asked for %q, which this test gave no answer to", body.Method)
		answer = failed(-32601, "no such method")
	}
	fmt.Fprint(w, answer)
}

// calls are what the adapter asked for, in the order it asked.
func (p *provider) calls() []request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]request(nil), p.asked...)
}

// answered is a provider's answer carrying a result.
func answered(result string) string {
	return `{"jsonrpc":"2.0","id":1,"result":` + result + `}`
}

// failed is a provider's answer carrying an error of its own.
func failed(code int, message string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":%d,"message":%q}}`, code, message)
}

// blockOf is a block as a provider writes one. A real answer carries twenty
// more fields, and the adapter reads these four.
func blockOf(height uint64, at int64) string {
	return fmt.Sprintf(`{"number":"0x%x","hash":%q,"parentHash":%q,"timestamp":"0x%x"}`,
		height, hashOf(height), hashOf(height-1), at)
}

// hashOf stands in for the hash of the block at a height, so that a test can
// say which block it means.
func hashOf(height uint64) string {
	return fmt.Sprintf("0x%064x", height)
}

func TestIdentity_IsWhatTheChainCallsItselfWrittenAsADocumentWritesIt(t *testing.T) {
	t.Parallel()
	evm, provider := opening(t, map[string]string{"eth_chainId": answered(`"0x89"`)})

	identity, err := evm.Identity(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if identity != "137" {
		t.Errorf("the chain calls itself %q, and 0x89 is 137", identity)
	}
	if calls := provider.calls(); len(calls) != 1 || calls[0].method != "eth_chainId" {
		t.Errorf("the provider was asked %v", calls)
	}
}

func TestHead_ReadsTheNewestBlockAndTheNewestOneThatStays(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getBlockByNumber latest":    answered(blockOf(0x100, 1788265269)),
		"eth_getBlockByNumber finalized": answered(blockOf(0xf0, 1788264969)),
	})

	head, err := evm.Head(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	want := chain.Head{
		Latest: chain.Block{Height: 0x100, Hash: hashOf(0x100), Parent: hashOf(0xff), Time: time.Unix(1788265269, 0).UTC()},
		Final:  chain.Block{Height: 0xf0, Hash: hashOf(0xf0), Parent: hashOf(0xef), Time: time.Unix(1788264969, 0).UTC()},
	}
	if head != want {
		t.Errorf("the head read as %+v, want %+v", head, want)
	}
}

// A provider that will not answer for the finalized block is one this does not
// read a chain through, and which way it declines is not worth telling apart:
// the tag is unknown to some and an error to others, and the codes they refuse
// it with are the codes they refuse a wide span with.
func TestHead_SaysNoFinalBlockWhereAProviderWillNotNameOne(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"nothing":              answered("null"),
		"an unknown parameter": failed(-32602, "invalid argument 0: hex string without 0x prefix"),
		"no such block":        failed(-32000, "finalized block not found"),
		"a block with no hash": answered(fmt.Sprintf(`{"number":"0xf0","parentHash":%q,"timestamp":"0x6a96c335"}`, hashOf(0xef))),
	} {
		t.Run(name, func(t *testing.T) {
			evm, _ := opening(t, map[string]string{
				"eth_getBlockByNumber latest":    answered(blockOf(0x100, 1788265269)),
				"eth_getBlockByNumber finalized": answer,
			})

			_, err := evm.Head(t.Context())

			if !errors.Is(err, chain.ErrNoFinal) {
				t.Errorf("Head gave %v, want a chain with no final block", err)
			}
			if errors.Is(err, chain.ErrTooWide) {
				t.Errorf("Head gave %v, and nothing here asked for a span", err)
			}
		})
	}
}

// A provider that will not take the call now is not a provider without a final
// block. Reading it as one would stop the observer on a chain it could read
// again in a minute.
func TestHead_KeepsWhatAProviderSaidAboutTheRateOfCalls(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Params []any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Params) > 0 && body.Params[0] == "finalized" {
			w.Header().Set("retry-after", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, failed(-32005, "too many requests"))
			return
		}
		fmt.Fprint(w, answered(blockOf(0x100, 1788265269)))
	}))
	t.Cleanup(server.Close)
	evm := &network{client: opened(t, server.URL)}

	_, err := evm.Head(t.Context())

	var limited chain.RateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("Head gave %v, want a provider asking to be left alone", err)
	}
	if limited.RetryAfter != 7*time.Second {
		t.Errorf("the wait reads as %s, want 7s", limited.RetryAfter)
	}
	if errors.Is(err, chain.ErrNoFinal) {
		t.Errorf("Head gave %v, and the provider said nothing about finality", err)
	}
}

// What went wrong reading the newest block is not that the chain has no final
// one, and there is nothing to ask about finality until the newest block is in
// hand.
func TestHead_SaysNothingOfFinalityWhenTheNewestBlockIsWhatWentWrong(t *testing.T) {
	t.Parallel()
	evm, provider := opening(t, map[string]string{
		"eth_getBlockByNumber latest": answered("null"),
	})

	_, err := evm.Head(t.Context())

	if err == nil {
		t.Fatal("Head read a head out of a provider with no newest block")
	}
	if errors.Is(err, chain.ErrNoFinal) {
		t.Errorf("Head gave %v, and nothing was asked about the final block", err)
	}
	if calls := provider.calls(); len(calls) != 1 {
		t.Errorf("the provider was asked %v", calls)
	}
}

func TestBlock_ReadsTheBlockAtAHeightAndAsksForNoTransactions(t *testing.T) {
	t.Parallel()
	evm, provider := opening(t, map[string]string{
		"eth_getBlockByNumber 0x89": answered(blockOf(0x89, 1788265269)),
	})

	block, err := evm.Block(t.Context(), 0x89)

	if err != nil {
		t.Fatal(err)
	}
	want := chain.Block{Height: 0x89, Hash: hashOf(0x89), Parent: hashOf(0x88), Time: time.Unix(1788265269, 0).UTC()}
	if block != want {
		t.Errorf("the block read as %+v, want %+v", block, want)
	}
	// The transactions of a block are what the observer never reads: it takes
	// the receipts of the keys it recognised, and a block with them in is the
	// same answer several hundred times over.
	if params := provider.calls()[0].params; len(params) != 2 || params[0] != "0x89" || params[1] != false {
		t.Errorf("the provider was asked for %v", params)
	}
}

func TestBlock_RefusesAnAnswerThatIdentifiesNoBlock(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"nothing":                   answered("null"),
		"a block with no hash":      answered(fmt.Sprintf(`{"number":"0x89","parentHash":%q,"timestamp":"0x6a96c335"}`, hashOf(0x88))),
		"a hash of the wrong width": answered(fmt.Sprintf(`{"number":"0x89","hash":"0xabcd","parentHash":%q,"timestamp":"0x6a96c335"}`, hashOf(0x88))),
	} {
		t.Run(name, func(t *testing.T) {
			evm, _ := opening(t, map[string]string{"eth_getBlockByNumber 0x89": answer})

			if block, err := evm.Block(t.Context(), 0x89); err == nil {
				t.Errorf("Block read %+v out of %s", block, answer)
			}
		})
	}
}

// A chain counts whole seconds, and a count no clock reaches is not one of
// them. Reading it as a time would stamp everything in that block with a
// moment somewhere before the chain existed.
func TestBlock_RefusesABlockStampedPastWhatATimeHolds(t *testing.T) {
	t.Parallel()
	evm, _ := opening(t, map[string]string{
		"eth_getBlockByNumber 0x89": answered(fmt.Sprintf(
			`{"number":"0x89","hash":%q,"parentHash":%q,"timestamp":"0xffffffffffffffff"}`, hashOf(0x89), hashOf(0x88))),
	})

	if block, err := evm.Block(t.Context(), 0x89); err == nil {
		t.Errorf("the block read as %+v", block)
	}
}

func TestImplementation_IsWhatSitsInTheSlotThatNamesTheCodeAProxyRuns(t *testing.T) {
	t.Parallel()
	asset := strings.ToLower(checksummed[0])
	behind := strings.ToLower(checksummed[1])
	evm, provider := opening(t, map[string]string{
		"eth_getStorageAt": answered(`"0x000000000000000000000000` + behind[2:] + `"`),
	})

	implementation, err := evm.Implementation(t.Context(), asset)

	if err != nil {
		t.Fatal(err)
	}
	if implementation != behind {
		t.Errorf("the code behind %s reads as %q, want %q", asset, implementation, behind)
	}
	want := []any{asset, implementationSlot, "latest"}
	if params := provider.calls()[0].params; fmt.Sprint(params) != fmt.Sprint(want) {
		t.Errorf("the provider was asked for %v, want %v", params, want)
	}
}

func TestImplementation_RefusesASlotHoldingSomethingOtherThanAnAddress(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"a word with more than an address in it": answered(`"0x1000000000000000000000000000000000000000000000000000000000000001"`),
		"something that is not a word":           answered(`"0x01"`),
	} {
		t.Run(name, func(t *testing.T) {
			evm, _ := opening(t, map[string]string{"eth_getStorageAt": answer})

			if got, err := evm.Implementation(t.Context(), strings.ToLower(checksummed[0])); err == nil {
				t.Errorf("Implementation read %q out of %s", got, answer)
			}
		})
	}
}

// An asset is asked about by reference, and a reference that is not an address
// is one nothing on this chain answers for. Asking anyway would put whatever
// it holds into a request.
func TestImplementation_RefusesAnAssetThatIsNoAddressWithoutAsking(t *testing.T) {
	t.Parallel()
	evm, provider := opening(t, map[string]string{})

	if _, err := evm.Implementation(t.Context(), "jpyc"); err == nil {
		t.Error("Implementation took an asset that is not an address")
	}
	if calls := provider.calls(); len(calls) != 0 {
		t.Errorf("the provider was asked %v", calls)
	}
}

func TestKind_OpensAChainOnTheEndpointItIsGivenAndNothingElse(t *testing.T) {
	t.Parallel()
	if Kind.Name != "evm" {
		t.Errorf("the kind is named %q", Kind.Name)
	}
	if Kind.Normalize == nil || Kind.Open == nil {
		t.Fatal("a kind needs both of its functions")
	}
	opened, err := Kind.Open(chain.Settings{Name: "polygon", ChainID: "137", RPC: "https://polygon.example/rpc"})
	if err != nil {
		t.Fatal(err)
	}
	if opened == nil {
		t.Fatal("Open gave no chain and no error")
	}
	for _, endpoint := range []string{"", "http://polygon.example/rpc", "https://:8545/", "not a URL"} {
		if _, err := Kind.Open(chain.Settings{Name: "polygon", RPC: endpoint}); err == nil {
			t.Errorf("Open took %q", endpoint)
		}
	}
}

// EIP-1967 puts the implementation one below the hash of its own name, so that
// no field of a contract lands on it.
func TestImplementationSlot_IsOneBelowTheHashEIP1967Names(t *testing.T) {
	t.Parallel()
	below := new(big.Int).Sub(new(big.Int).SetBytes(keccak("eip1967.proxy.implementation")), big.NewInt(1))

	if want := "0x" + hex.EncodeToString(below.FillBytes(make([]byte, wordBytes))); implementationSlot != want {
		t.Errorf("the slot is written as %s, want %s", implementationSlot, want)
	}
}
