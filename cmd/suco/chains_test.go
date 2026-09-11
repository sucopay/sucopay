package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/payment"
)

// listing is a configuration holding the networks named and one asset on each,
// named after its network so that a test can tell them apart.
func listing(t *testing.T, networks map[string]config.Network) config.Config {
	t.Helper()
	assets := config.Assets{}
	for name := range networks {
		asset, err := payment.NewAsset(payment.Network(name),
			"0x0000000000000000000000000000000000000001", "JPYC", 18)
		if err != nil {
			t.Fatal(err)
		}
		assets[name+"-token"] = asset
	}
	return config.Config{Networks: networks, Assets: assets}
}

// simulated is a network of the kind that runs inside the process, which has
// neither an endpoint nor a chain to identify.
func simulatedNetwork() config.Network {
	return config.Network{Kind: "simulated", Poll: 3 * time.Second, Width: 100}
}

// A network is opened for each of the chains the assets settle on, in the
// order of their names, with the settings the document gave it.
func TestOpenChains_OpensOneNetworkPerChainAnAssetSettlesOn(t *testing.T) {
	t.Parallel()
	opened, err := openChains(listing(t, map[string]config.Network{
		"beta":  simulatedNetwork(),
		"alpha": simulatedNetwork(),
	}))

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(opened) != 2 {
		t.Fatalf("opened %d networks, want 2", len(opened))
	}
	// In one order, so that what a deployment starts, and the order it says
	// so in, do not depend on where a map put things.
	if opened[0].Name != "alpha" || opened[1].Name != "beta" {
		t.Errorf("opened %s then %s, want them in the order of their names",
			opened[0].Name, opened[1].Name)
	}
	for _, n := range opened {
		if n.Chain == nil {
			t.Errorf("%s was opened onto nothing", n.Name)
		}
		if n.Poll != 3*time.Second || n.Width != 100 {
			t.Errorf("%s reads %s apart at %d blocks, want the document's 3s and 100",
				n.Name, n.Poll, n.Width)
		}
		if want := n.Name + "-token"; n.Assets[want].Network() != payment.Network(n.Name) {
			t.Errorf("%s carries %v, want the asset the document lists under %s",
				n.Name, n.Assets, want)
		}
	}
}

// A chain that says which one it is is held to it. One that has none to say
// is held to nothing: a simulated chain is whatever the process makes it.
func TestOpenChains_ExpectsTheChainTheDocumentNamesAndNoOther(t *testing.T) {
	t.Parallel()
	evm := config.Network{Kind: "evm", ChainID: 137,
		RPC: config.Endpoints{Own: "https://example.invalid/rpc"}, Poll: time.Second, Width: 10}
	opened, err := openChains(listing(t, map[string]config.Network{
		"polygon": evm,
		"local":   simulatedNetwork(),
	}))

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	want := map[string]string{"local": "", "polygon": "137"}
	for _, n := range opened {
		if n.Want != want[n.Name] {
			t.Errorf("%s expects the chain to call itself %q, want %q", n.Name, n.Want, want[n.Name])
		}
	}
}

// A kind this build cannot open is refused rather than left out, which would
// be a network the document declares and nothing reads.
func TestOpenChains_RefusesAKindThisBuildCannotOpen(t *testing.T) {
	t.Parallel()
	_, err := openChains(listing(t, map[string]config.Network{
		"local": {Kind: "ripple", Poll: time.Second, Width: 10},
	}))

	if err == nil {
		t.Fatal("a network of a kind nothing can open was opened")
	}
	for _, want := range []string{"local", "ripple"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

// An endpoint may carry a key, so a refusal names the network and repeats
// nothing of where it was to be reached.
func TestOpenChains_RepeatsNothingOfAnEndpointItRefuses(t *testing.T) {
	t.Parallel()
	const endpoint = "http://reader.example.invalid/v1?key=secret"
	_, err := openChains(listing(t, map[string]config.Network{
		"polygon": {Kind: "evm", ChainID: 137, RPC: config.Endpoints{Own: endpoint},
			Poll: time.Second, Width: 10},
	}))

	if err == nil {
		t.Fatal("an endpoint reached in the clear over the network was opened")
	}
	if !strings.Contains(err.Error(), "polygon") {
		t.Errorf("error does not name the network: %v", err)
	}
	for _, part := range []string{"reader.example.invalid", "key=secret", "secret", "v1"} {
		if strings.Contains(err.Error(), part) {
			t.Errorf("error holds %q of the endpoint: %v", part, err)
		}
	}
}

// An asset on a network the document does not declare is one nothing could
// read, and a configuration holding one was not built by resolving a document.
func TestOpenChains_RefusesAnAssetOnANetworkNothingDeclares(t *testing.T) {
	t.Parallel()
	cfg := listing(t, map[string]config.Network{"local": simulatedNetwork()})
	asset, err := payment.NewAsset("elsewhere", "0x01", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Assets["stray"] = asset

	if _, err := openChains(cfg); err == nil {
		t.Fatal("an asset on a network nothing declares was opened")
	} else if !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("error does not name the network: %v", err)
	}
}

// A round reads one endpoint, and which one is not a matter of taste: the
// operator's own node answers for itself, and a deployment without one reads
// the first of the others. The endpoint each case reaches is one Open refuses,
// so the refusal is what says which one was read.
func TestOpenChains_ReadsTheOwnNodeWhenThereIsOneAndTheFirstOfOthersOtherwise(t *testing.T) {
	t.Parallel()
	const refused = "http://reader.example.invalid/v1?key=secret"
	const accepted = "https://reader.example.invalid/v1"
	for what, endpoints := range map[string]config.Endpoints{
		"own over others":     {Own: refused, Others: []string{accepted}},
		"the first of others": {Others: []string{refused, accepted}},
	} {
		t.Run(what, func(t *testing.T) {
			_, err := openChains(listing(t, map[string]config.Network{
				"polygon": {Kind: "evm", ChainID: 137, RPC: endpoints,
					Poll: time.Second, Width: 10},
			}))

			if err == nil {
				t.Fatal("the endpoint this reads is not the one it was given")
			}
		})
	}
}
