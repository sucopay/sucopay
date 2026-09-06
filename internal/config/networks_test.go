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
	network := map[string]any{"chain_id": uint64(137), "rpc": polygonRPC}
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
	if rpc := got.Config.Networks["polygon"].RPC; rpc != polygonRPC {
		t.Errorf("networks.polygon.rpc = %q, want the endpoint as written", rpc)
	}
}

func TestResolve_RequiresAChainIDAndAnRPCOfAnEVMNetwork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		doc   map[string]any
		paths []string
	}{
		{"without a chain id", evmNetwork(map[string]any{"chain_id": nil}), []string{"networks.polygon.chain_id"}},
		{"without an rpc", evmNetwork(map[string]any{"rpc": nil}), []string{"networks.polygon.rpc"}},
		{
			"without either",
			map[string]any{"networks": map[string]any{"polygon": map[string]any{"kind": "evm"}}},
			[]string{"networks.polygon.chain_id", "networks.polygon.rpc"},
		},
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
		{"an rpc", map[string]any{"rpc": "http://127.0.0.1:8545"}, "networks.local.rpc"},
		{"an empty rpc", map[string]any{"rpc": ""}, "networks.local.rpc"},
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

func TestResolve_AcceptsAnRPCOverHTTPSOrOverHTTPToThisMachine(t *testing.T) {
	t.Parallel()
	accepted := []string{
		"https://polygon.example/v2/key",
		"https://user:key@polygon.example/",
		"http://localhost:8545",
		"http://127.0.0.1:8545",
		"http://[::1]:8545",
	}
	for _, rpc := range accepted {
		t.Run(rpc, func(t *testing.T) {
			got := mustResolve(t, evmNetwork(map[string]any{"rpc": rpc}), noEnv).Config

			if got.Networks["polygon"].RPC != rpc {
				t.Errorf("networks.polygon.rpc = %q, want %q", got.Networks["polygon"].RPC, rpc)
			}
		})
	}
	refused := []struct{ rpc, says string }{
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
		t.Run(c.rpc, func(t *testing.T) {
			_, err := config.Resolve(evmNetwork(map[string]any{"rpc": c.rpc}), noEnv)

			p := wantProblemAt(t, err, "networks.polygon.rpc")
			if !strings.Contains(p.Message, c.says) {
				t.Errorf("message %q does not say %q", p.Message, c.says)
			}
			if c.rpc != "" && strings.Contains(p.Message, c.rpc) {
				t.Errorf("message %q repeats the endpoint, which is a secret", p.Message)
			}
		})
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
		{"networks.polygon.rpc", "set"},
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
		if l.Path == "networks.local.chain_id" || l.Path == "networks.local.rpc" {
			t.Errorf("the report lists %s, which a simulated network does not take", l.Path)
		}
		if strings.Contains(l.Value, "hunter2") {
			t.Errorf("the endpoint appears in the report at %s", l.Path)
		}
	}
}
