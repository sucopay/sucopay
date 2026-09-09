package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/kinds"
	"github.com/sucopay/sucopay/internal/config"
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
	assets := map[string]map[string]payment.Asset{}
	for name, asset := range cfg.Assets {
		on := string(asset.Network())
		if _, declared := cfg.Networks[on]; !declared {
			return nil, fmt.Errorf("asset %s is on %s, which the document does not declare", name, on)
		}
		if assets[on] == nil {
			assets[on] = map[string]payment.Asset{}
		}
		assets[on][name] = asset
	}

	out := make([]observe.Network, 0, len(assets))
	for _, name := range slices.Sorted(maps.Keys(assets)) {
		n := cfg.Networks[name]
		kind, known := kinds.Lookup(n.Kind)
		if !known {
			return nil, fmt.Errorf("network %s is of kind %s, which this build cannot open", name, n.Kind)
		}
		// The endpoint is a secret and the kind's own refusal carries none of
		// it. What is added here is which network was being opened, since a
		// deployment reads several and the refusal says nothing until it says
		// which one.
		read, err := kind.Open(chain.Settings{Name: name, ChainID: identity(n), RPC: n.RPC})
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
