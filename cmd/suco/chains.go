package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/kinds"
	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/finality"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
)

// openChains makes the adapter for each chain the document's assets settle
// on, and hands back what reads each of them.
//
// By the assets rather than by the networks: a network is read so that a
// payment on it can be seen arriving, and one no asset refers to has no
// payment to see. The document is refused for declaring such a network, so
// what this leaves out is what that refusal would have caught.
//
// In the order of the names, so that what a deployment starts, and the order
// it says so in, read the same each time.
func openChains(cfg config.Config) ([]observe.Network, error) {
	names, assets, err := settled(cfg)
	if err != nil {
		return nil, err
	}

	out := make([]observe.Network, 0, len(names))
	for _, name := range names {
		n := cfg.Networks[name]
		kind, known := kinds.Lookup(n.Kind)
		if !known {
			return nil, fmt.Errorf("network %s is of kind %s, which this build cannot open", name, n.Kind)
		}
		// The endpoint is a secret and the kind's own refusal carries none of
		// it. What is added here is which network was being opened, since a
		// deployment reads several and the refusal says nothing until it says
		// which one.
		read, err := kind.Open(chain.Settings{Name: name, ChainID: identity(n), RPC: endpoint(n).Expose()})
		if err != nil {
			return nil, fmt.Errorf("network %s: %w", name, err)
		}
		out = append(out, observe.Network{
			Name:   name,
			Want:   identity(n),
			Chain:  read,
			Assets: assets[name],
			Poll:   n.Poll,
			Width:  n.Width,
		})
	}
	return out, nil
}

// settled are the networks the document's assets settle on, in name order,
// with the assets on each. By the assets rather than by the networks, for the
// reason [openChains] gives.
func settled(cfg config.Config) ([]string, map[string]map[string]payment.Asset, error) {
	assets := map[string]map[string]payment.Asset{}
	for name, asset := range cfg.Assets {
		on := string(asset.Network())
		if _, declared := cfg.Networks[on]; !declared {
			return nil, nil, fmt.Errorf("asset %s is on %s, which the document does not declare", name, on)
		}
		if assets[on] == nil {
			assets[on] = map[string]payment.Asset{}
		}
		assets[on][name] = asset
	}
	return slices.Sorted(maps.Keys(assets)), assets, nil
}

// openSettling makes the adapters each network is asked through when a
// recorded transfer is asked about again, and hands back what decides for each
// network.
//
// The same networks [openChains] reads, and not the same endpoints. What reads
// a chain reads one endpoint, because a cursor is a place in a chain as one
// provider tells it. What decides asks the endpoints it has until as many as
// agreement takes have agreed, because a deployment that asks one third
// party is a deployment that believes it.
func openSettling(cfg config.Config) ([]finality.Network, error) {
	names, _, err := settled(cfg)
	if err != nil {
		return nil, err
	}

	out := make([]finality.Network, 0, len(names))
	for _, name := range names {
		n := cfg.Networks[name]
		kind, known := kinds.Lookup(n.Kind)
		if !known {
			return nil, fmt.Errorf("network %s is of kind %s, which this build cannot open", name, n.Kind)
		}
		asked := make([]chain.Chain, 0, 1)
		for _, url := range endpoints(n) {
			// The endpoint is a secret, and what is added here is which
			// network was being opened, for the reason [openChains] gives.
			read, err := kind.Open(chain.Settings{Name: name, ChainID: identity(n), RPC: url.Expose()})
			if err != nil {
				return nil, fmt.Errorf("network %s: %w", name, err)
			}
			asked = append(asked, read)
		}
		out = append(out, finality.Network{
			Name:       name,
			Endpoints:  asked,
			Agreements: agreements(n),
			Recheck:    n.Finality.Recheck,
			Misses:     n.Finality.Misses,
		})
	}
	return out, nil
}

// endpoints are the ones a network's settling is decided through. The
// operator's own node alone when there is one: a node the operator runs is the
// one they already trust, and asking others beside it would hold a payment
// behind whichever of them is slowest. All of the others when there is no own
// node, in order, because agreement between them is what stands in for that
// trust, and the ones past the agreement are the spares.
//
// One endpoint with nothing in it where the document names none, which is a
// kind that reaches no chain.
func endpoints(n config.Network) []config.Hidden {
	if n.RPC.Own != "" {
		return []config.Hidden{n.RPC.Own}
	}
	if len(n.RPC.Others) > 0 {
		return n.RPC.Others
	}
	return []config.Hidden{""}
}

// agreements is how many of a network's endpoints have to say the same thing.
// One where the operator runs a node, and one where there is nothing to ask
// but the chain in the process; [finality.Agreements] where the network is
// reached only through third parties.
func agreements(n config.Network) int {
	if n.RPC.Own == "" && len(n.RPC.Others) > 0 {
		return finality.Agreements
	}
	return 1
}

// identity is what the chain is expected to call itself, written the way it
// writes it, and nothing where the document names none. Zero stands for none:
// a kind that has no chain to identify cannot be given a chain id, which the
// document is held to.
func identity(n config.Network) string {
	if n.ChainID == 0 {
		return ""
	}
	return strconv.FormatUint(n.ChainID, 10)
}

// endpoint is where a round reads this network. The operator's own node when
// there is one, and the first of the others otherwise. One endpoint, and the
// same one every round: a cursor is a place in a chain as one provider tells
// it, and moving between providers would leave the position meaning something
// else. The rest of the others are asked by [openSettling], where several
// answers to one question are the point.
func endpoint(n config.Network) config.Hidden {
	if n.RPC.Own != "" {
		return n.RPC.Own
	}
	if len(n.RPC.Others) > 0 {
		return n.RPC.Others[0]
	}
	return ""
}
