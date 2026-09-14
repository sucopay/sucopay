package config_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/config"
)

// polygonRPC is an endpoint of the shape an operator writes, with the key a
// provider issues on the path. A problem or a report carrying it took it
// from a secret setting.
const polygonRPC = "https://polygon.example/v2/apikey-hunter2"

// evmNetwork is a network of the default kind with every setting it needs,
// with the fields given overriding or, when nil, removing one.
func evmNetwork(fields map[string]any) map[string]any {
	network := map[string]any{"chain_id": uint64(137), "rpc": map[string]any{"own": polygonRPC}}
	for k, v := range fields {
		if v == nil {
			delete(network, k)
			continue
		}
		network[k] = v
	}
	return map[string]any{"networks": map[string]any{"polygon": network}}
}

func TestResolve_TakesANetworkWithoutAKindForEVM(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, evmNetwork(nil), noEnv)

	if kind := got.Config.Networks["polygon"].Kind; kind != "evm" {
		t.Errorf("networks.polygon.kind = %q, want evm", kind)
	}
	if source, _ := got.SourceOf("networks.polygon.kind"); source.Origin != config.FromDefault {
		t.Errorf("networks.polygon.kind came from %v, want default", source.Origin)
	}
	if id := got.Config.Networks["polygon"].ChainID; id != 137 {
		t.Errorf("networks.polygon.chain_id = %d, want 137", id)
	}
	if own := got.Config.Networks["polygon"].RPC.Own; own != polygonRPC {
		t.Errorf("networks.polygon.rpc.own = %q, want the endpoint as written", own)
	}
}

func TestResolve_RequiresAChainIDOfAnEVMNetwork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		doc   map[string]any
		paths []string
	}{
		{"without a chain id", evmNetwork(map[string]any{"chain_id": nil}), []string{"networks.polygon.chain_id"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(c.doc, noEnv)

			var got []string
			for _, p := range problems(t, err) {
				got = append(got, p.Path)
				if !strings.Contains(p.Message, "required") {
					t.Errorf("message at %s = %q, want it to say the setting is required", p.Path, p.Message)
				}
			}
			if !slices.Equal(got, c.paths) {
				t.Errorf("problem paths = %v, want exactly %v", got, c.paths)
			}
		})
	}
}

func TestResolve_RefusesAChainIDOrAnRPCOnASimulatedNetwork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		extra map[string]any
		path  string
	}{
		{"a chain id", map[string]any{"chain_id": uint64(1)}, "networks.local.chain_id"},
		{"an own endpoint", map[string]any{"rpc": map[string]any{"own": "http://127.0.0.1:8545"}}, "networks.local.rpc"},
		{"others", map[string]any{"rpc": map[string]any{"others": []any{"https://polygon.example/"}}}, "networks.local.rpc"},
		// A kind that reaches no chain is told that, whatever shape the
		// setting it does not take was written in.
		{"an endpoint of the shape this setting no longer takes", map[string]any{"rpc": "http://127.0.0.1:8545"}, "networks.local.rpc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			network := map[string]any{"kind": "simulated"}
			for k, v := range c.extra {
				network[k] = v
			}
			doc := map[string]any{"networks": map[string]any{"local": network}}

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 {
				t.Fatalf("got %d problems, want 1: %v", len(got), got)
			}
			if got[0].Path != c.path {
				t.Errorf("path = %q, want %s", got[0].Path, c.path)
			}
			if !strings.Contains(got[0].Message, "simulated") {
				t.Errorf("message %q does not name the kind that has no such setting", got[0].Message)
			}
		})
	}
}

// Every endpoint a network holds is held to the same shape, whether an
// operator wrote it under own or as one of others. The path a problem names
// says which one, so that a document with four endpoints says which of them
// to fix.
func TestResolve_AcceptsAnEndpointOverHTTPSOrOverHTTPToThisMachine(t *testing.T) {
	t.Parallel()
	accepted := []string{
		"https://polygon.example/v2/key",
		"https://user:key@polygon.example/",
		"http://localhost:8545",
		"http://127.0.0.1:8545",
		"http://[::1]:8545",
	}
	for _, endpoint := range accepted {
		t.Run("own "+endpoint, func(t *testing.T) {
			doc := evmNetwork(map[string]any{"rpc": map[string]any{"own": endpoint}})

			got := mustResolve(t, doc, noEnv).Config

			if own := got.Networks["polygon"].RPC.Own; own != endpoint {
				t.Errorf("networks.polygon.rpc.own = %q, want %q", own, endpoint)
			}
		})
		t.Run("others "+endpoint, func(t *testing.T) {
			doc := evmNetwork(map[string]any{"rpc": map[string]any{"others": []any{endpoint}}})

			got := mustResolve(t, doc, noEnv).Config

			if others := got.Networks["polygon"].RPC.Others; !slices.Equal(others, []string{endpoint}) {
				t.Errorf("networks.polygon.rpc.others = %v, want [%q]", others, endpoint)
			}
		})
	}
	refused := []struct{ endpoint, says string }{
		{"http://polygon.example:8545", "localhost"},
		{"http://localhost.example:8545", "localhost"},
		{"http://127.0.0.1.example:8545", "localhost"},
		{"http://10.0.0.1:8545", "loopback"},
		{"http://[fd00::1]:8545", "loopback"},
		{"ftp://polygon.example", "https"},
		{"polygon.example", "https"},
		{"", "https"},
		{"https:///v2/key", "host"},
		{"https://:8545/v2/key", "host"},
		{"http://[::1]-not:8545", "not a URL"},
	}
	for _, c := range refused {
		t.Run("own "+c.endpoint, func(t *testing.T) {
			doc := evmNetwork(map[string]any{"rpc": map[string]any{"own": c.endpoint}})

			_, err := config.Resolve(doc, noEnv)

			p := wantProblemAt(t, err, "networks.polygon.rpc.own")
			if !strings.Contains(p.Message, c.says) {
				t.Errorf("message %q does not say %q", p.Message, c.says)
			}
			if c.endpoint != "" && strings.Contains(p.Message, c.endpoint) {
				t.Errorf("message %q repeats the endpoint, which is a secret", p.Message)
			}
		})
	}
}

// An operator running a node can name third-party endpoints beside it, and a
// document holding both is read as both: which of them is asked is decided
// where the endpoints are opened, not here.
func TestResolve_ReadsANetworkHoldingAnOwnNodeAndOthers(t *testing.T) {
	t.Parallel()
	others := []string{"https://first.example/", "https://second.example/"}
	doc := evmNetwork(map[string]any{"rpc": map[string]any{
		"own": polygonRPC, "others": []any{others[0], others[1]},
	}})

	got := mustResolve(t, doc, noEnv).Config

	rpc := got.Networks["polygon"].RPC
	if rpc.Own != polygonRPC {
		t.Errorf("networks.polygon.rpc.own = %q, want %q", rpc.Own, polygonRPC)
	}
	if !slices.Equal(rpc.Others, others) {
		t.Errorf("networks.polygon.rpc.others = %v, want %v", rpc.Others, others)
	}
}

// The element that is wrong is named by its place in the list. A message that
// said only networks.polygon.rpc.others would leave an operator with four
// endpoints to compare by eye.
func TestResolve_NamesTheElementOfOthersThatIsNotAnEndpoint(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"rpc": map[string]any{"others": []any{
		"https://first.example/", "ftp://second.example/", "https://third.example/",
	}}})

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "networks.polygon.rpc.others[1]" {
		t.Fatalf("problems = %v, want one at networks.polygon.rpc.others[1]", got)
	}
	if strings.Contains(got[0].Message, "second.example") {
		t.Errorf("message %q repeats the endpoint, which is a secret", got[0].Message)
	}
}

// An endpoint is a secret wherever it sits, so an element of others takes a
// value from the environment the way own does, and a report shows neither.
func TestResolve_ReadsAnEndpointOfOthersFromTheEnvironment(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"rpc": map[string]any{
		"others": []any{"${SUCO_POLYGON_RPC_URL}"},
	}})
	env := envOf(map[string]string{"SUCO_POLYGON_RPC_URL": polygonRPC})

	got := mustResolve(t, doc, env)

	if others := got.Config.Networks["polygon"].RPC.Others; !slices.Equal(others, []string{polygonRPC}) {
		t.Errorf("networks.polygon.rpc.others = %v, want [%q]", others, polygonRPC)
	}
	line := lineAt(t, got.Report(), "networks.polygon.rpc.others[0]")
	if !line.Secret || line.Source.Origin != config.FromEnv {
		t.Errorf("report line = %+v, want a secret from the environment", line)
	}
}

// An evm network reaches a chain through at least one endpoint. Which of the
// two an operator gives is theirs to choose, and giving neither leaves nothing
// to read the chain with.
func TestResolve_RequiresAtLeastOneEndpointOfAnEVMNetwork(t *testing.T) {
	t.Parallel()
	for what, rpc := range map[string]any{
		"no rpc at all":   nil,
		"an empty rpc":    map[string]any{},
		"an empty others": map[string]any{"others": []any{}},
	} {
		t.Run(what, func(t *testing.T) {
			_, err := config.Resolve(evmNetwork(map[string]any{"rpc": rpc}), noEnv)

			p := wantProblemAt(t, err, "networks.polygon.rpc")
			if !strings.Contains(p.Message, "own") || !strings.Contains(p.Message, "others") {
				t.Errorf("message %q does not name the two settings that reach a chain", p.Message)
			}
		})
	}
}

// A document written for the earlier shape put a URL straight under rpc. The
// refusal names what takes its place, rather than reporting a key nobody reads.
func TestResolve_RefusesAnRPCThatIsNotAMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		what, found string
		rpc         any
	}{
		{"a URL", "text", "https://polygon.example/v2/key"},
		{"a list", "a list", []any{"https://polygon.example/"}},
		{"nothing", "text", ""},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			_, err := config.Resolve(evmNetwork(map[string]any{"rpc": c.rpc}), noEnv)

			p := wantProblemAt(t, err, "networks.polygon.rpc")
			if !strings.Contains(p.Message, "own") || !strings.Contains(p.Message, "others") {
				t.Errorf("message %q does not name what rpc takes", p.Message)
			}
			// What was written there instead, which is what separates this
			// from the refusal of a network that names no endpoint at all.
			if !strings.Contains(p.Message, c.found) {
				t.Errorf("message %q does not say it found %s", p.Message, c.found)
			}
			if s, isText := c.rpc.(string); isText && s != "" && strings.Contains(p.Message, s) {
				t.Errorf("message %q repeats the endpoint, which is a secret", p.Message)
			}
		})
	}
}

// A name is a path segment, and a segment ending in a place in a list names
// one. A name written that way would take every setting under it with it.
func TestResolve_RejectsANameHoldingABracket(t *testing.T) {
	t.Parallel()
	// Beside the one the asset settles on, so that the only thing wrong with
	// the document is the name.
	for section, bend := range map[string]func(map[string]any){
		"networks": func(doc map[string]any) {
			doc["networks"].(map[string]any)["local[0]"] = map[string]any{"kind": "simulated"}
		},
		"assets": func(doc map[string]any) {
			assets := doc["assets"].(map[string]any)
			assets["jpyc[0]"] = assets["jpyc"]
			delete(assets, "jpyc")
		},
	} {
		t.Run(section, func(t *testing.T) {
			doc := assetDocument(nil)
			bend(doc)

			_, err := config.Resolve(doc, noEnv)

			got := problems(t, err)
			if len(got) != 1 || got[0].Path != section {
				t.Fatalf("problems = %v, want one at %s and none for the keys under the name", got, section)
			}
			if !strings.Contains(got[0].Message, "bracket") {
				t.Errorf("message %q does not say what is wrong with the name", got[0].Message)
			}
		})
	}
}

// A network reached only through others still reports an own, as not set. A
// row missing entirely would read as a setting this kind does not take, which
// is what a simulated network's rows look like.
func TestReport_ShowsAnOwnThatIsNotSet(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"rpc": map[string]any{
		"others": []any{"https://polygon.example/"},
	}})

	lines := mustResolve(t, doc, noEnv).Report()

	own := lineAt(t, lines, "networks.polygon.rpc.own")
	if own.Value != "not set" || !own.Secret {
		t.Errorf("networks.polygon.rpc.own = %+v, want a secret reading not set", own)
	}
	if others := lineAt(t, lines, "networks.polygon.rpc.others[0]"); others.Value != "set" {
		t.Errorf("networks.polygon.rpc.others[0] = %+v, want it to read set", others)
	}
}

// An element that is not text is refused once. Reading it on as an empty
// string would refuse it a second time for its shape, and an operator would
// have two problems to chase for one mistake.
func TestResolve_RefusesAnElementOfOthersThatIsNotText(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"rpc": map[string]any{"others": []any{uint64(8545)}}})

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 || got[0].Path != "networks.polygon.rpc.others[0]" {
		t.Fatalf("problems = %v, want exactly one at networks.polygon.rpc.others[0]", got)
	}
	if !strings.Contains(got[0].Message, "want text") {
		t.Errorf("message %q does not say text is wanted", got[0].Message)
	}
}

// others is a list of endpoints. A document giving one endpoint without the
// list is refused rather than read as a list of one.
func TestResolve_RefusesOthersThatIsNotAList(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"rpc": map[string]any{"others": polygonRPC}})

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "networks.polygon.rpc.others")
	if !strings.Contains(p.Message, "list") {
		t.Errorf("message %q does not say a list is wanted", p.Message)
	}
	if strings.Contains(p.Message, polygonRPC) {
		t.Errorf("message %q repeats the endpoint, which is a secret", p.Message)
	}
}

func TestResolve_GivesPollAndWidthTheirDefaults(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, evmNetwork(nil), noEnv)

	network := got.Config.Networks["polygon"]
	if network.Poll != config.DefaultPoll {
		t.Errorf("networks.polygon.poll = %v, want %v", network.Poll, config.DefaultPoll)
	}
	if network.Width != config.DefaultWidth {
		t.Errorf("networks.polygon.width = %d, want %d", network.Width, config.DefaultWidth)
	}
	for _, path := range []string{"networks.polygon.poll", "networks.polygon.width"} {
		if source, _ := got.SourceOf(path); source.Origin != config.FromDefault {
			t.Errorf("%s came from %v, want default", path, source.Origin)
		}
	}
}

func TestResolve_ReadsPollAsADurationOfAtLeastOneSecond(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, evmNetwork(map[string]any{"poll": "1m30s"}), noEnv).Config
	if got.Networks["polygon"].Poll != 90*time.Second {
		t.Errorf("networks.polygon.poll = %v, want 1m30s", got.Networks["polygon"].Poll)
	}

	refused := []struct {
		name string
		poll any
	}{
		{"below one second", "900ms"},
		{"a number without a unit", uint64(3)},
		{"text without a unit", "3"},
		{"text that is not a duration", "three seconds"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(evmNetwork(map[string]any{"poll": c.poll}), noEnv)

			p := wantProblemAt(t, err, "networks.polygon.poll")
			if !strings.Contains(p.Message, "1s") && !strings.Contains(p.Message, "3s") {
				t.Errorf("message %q shows no duration the setting would take", p.Message)
			}
		})
	}
}

func TestResolve_KeepsWidthWithinItsBounds(t *testing.T) {
	t.Parallel()
	for _, width := range []int{config.MinWidth, config.MaxWidth} {
		got := mustResolve(t, evmNetwork(map[string]any{"width": uint64(width)}), noEnv).Config
		if got.Networks["polygon"].Width != width {
			t.Errorf("networks.polygon.width = %d, want %d", got.Networks["polygon"].Width, width)
		}
	}
	bounds := fmt.Sprintf("%d-%d", config.MinWidth, config.MaxWidth)
	for _, width := range []any{uint64(config.MinWidth - 1), uint64(config.MaxWidth + 1), int64(-1)} {
		_, err := config.Resolve(evmNetwork(map[string]any{"width": width}), noEnv)

		p := wantProblemAt(t, err, "networks.polygon.width")
		if !strings.Contains(p.Message, bounds) {
			t.Errorf("message %q does not name the bounds %s", p.Message, bounds)
		}
	}
}

func TestResolve_ReadsAChainIDOfOneOrMore(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, evmNetwork(map[string]any{"chain_id": "137"}), noEnv).Config
	if got.Networks["polygon"].ChainID != 137 {
		t.Errorf("networks.polygon.chain_id = %d, want 137 read from text", got.Networks["polygon"].ChainID)
	}

	for _, id := range []any{uint64(0), int64(-1), "polygon"} {
		_, err := config.Resolve(evmNetwork(map[string]any{"chain_id": id}), noEnv)

		wantProblemAt(t, err, "networks.polygon.chain_id")
	}
}

func TestNetworkKinds_ListsEveryKindInNameOrder(t *testing.T) {
	t.Parallel()
	if got := config.NetworkKinds(); !slices.Equal(got, []string{"evm", "simulated"}) {
		t.Errorf("NetworkKinds() = %v, want [evm simulated]", got)
	}
}

func TestReport_ShowsTheSettingsANetworksKindTakes(t *testing.T) {
	t.Parallel()
	doc := evmNetwork(map[string]any{"poll": "5s", "width": uint64(50)})
	doc["networks"].(map[string]any)["local"] = map[string]any{"kind": "simulated"}

	lines := mustResolve(t, doc, noEnv).Report()

	for _, want := range []struct{ path, value string }{
		{"networks.polygon.kind", "evm"},
		{"networks.polygon.chain_id", "137"},
		{"networks.polygon.rpc.own", "set"},
		{"networks.polygon.poll", "5s"},
		{"networks.polygon.width", "50"},
		{"networks.local.kind", "simulated"},
		{"networks.local.poll", config.DefaultPoll.String()},
		{"networks.local.width", strconv.Itoa(config.DefaultWidth)},
	} {
		if got := lineAt(t, lines, want.path).Value; got != want.value {
			t.Errorf("%s reads %q, want %q", want.path, got, want.value)
		}
	}
	for _, l := range lines {
		if l.Path == "networks.local.chain_id" || strings.HasPrefix(l.Path, "networks.local.rpc") {
			t.Errorf("the report lists %s, which a simulated network does not take", l.Path)
		}
		if strings.Contains(l.Value, "hunter2") {
			t.Errorf("the endpoint appears in the report at %s", l.Path)
		}
	}
}

// finality is a network whose settling is written out, for the tests that read
// those settings back.
func finality(fields map[string]any) map[string]any {
	return evmNetwork(map[string]any{"finality": fields})
}

func TestResolve_GivesFinalityItsDefaults(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, evmNetwork(nil), noEnv)

	network := got.Config.Networks["polygon"]
	if network.Finality.Recheck != config.DefaultFinalityRecheck {
		t.Errorf("finality.recheck = %v, want %v", network.Finality.Recheck, config.DefaultFinalityRecheck)
	}
	if network.Finality.Misses != config.DefaultFinalityMisses {
		t.Errorf("finality.misses = %d, want %d", network.Finality.Misses, config.DefaultFinalityMisses)
	}
	for _, path := range []string{
		"networks.polygon.finality.recheck",
		"networks.polygon.finality.misses",
	} {
		if source, _ := got.SourceOf(path); source.Origin != config.FromDefault {
			t.Errorf("%s came from %v, want default", path, source.Origin)
		}
	}
}

func TestResolve_ReadsWhatADocumentSaysAboutFinality(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, finality(map[string]any{
		"recheck": "30s", "misses": uint64(4)}), noEnv).Config

	network := got.Networks["polygon"]
	if network.Finality.Recheck != 30*time.Second {
		t.Errorf("finality.recheck = %v, want 30s", network.Finality.Recheck)
	}
	if network.Finality.Misses != 4 {
		t.Errorf("finality.misses = %d, want 4", network.Finality.Misses)
	}
}

// A recheck spent on nothing is somebody else's endpoint spent on nothing.
func TestResolve_KeepsTheRecheckAboveItsFloor(t *testing.T) {
	t.Parallel()
	_, err := config.Resolve(finality(map[string]any{"recheck": "500ms"}), noEnv)

	wantProblemAt(t, err, "networks.polygon.finality.recheck")
}

// Not finding a transfer once is not the transfer being gone: the endpoint may
// be reading a state it has not finished replacing. A document that asks to
// give up the first time is asking for the thing the count exists to prevent.
func TestResolve_RefusesToGiveUpOnATransferTheFirstTimeNothingIsFound(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, finality(map[string]any{
		"misses": uint64(config.MinFinalityMisses)}), noEnv).Config
	if got.Networks["polygon"].Finality.Misses != config.MinFinalityMisses {
		t.Errorf("finality.misses = %d, want %d",
			got.Networks["polygon"].Finality.Misses, config.MinFinalityMisses)
	}

	_, err := config.Resolve(finality(map[string]any{"misses": uint64(1)}), noEnv)

	p := wantProblemAt(t, err, "networks.polygon.finality.misses")
	if !strings.Contains(p.Message, strconv.Itoa(config.MinFinalityMisses)) {
		t.Errorf("message %q does not show the fewest a document may set", p.Message)
	}
}
