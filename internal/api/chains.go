package api

import "slices"

// Chains is what each network of a deployment and each asset on them have
// come to, in the one word apiece a probe answers with.
//
// What reads the chains writes these words down as it goes, so answering a
// probe reads no chain. A probe is public, and reaching a provider to answer
// one would let whoever can reach the port spend this deployment's share of
// somebody else's endpoint.
//
// Nil in an instance with no database, which reads no chain: it has nowhere
// to write down how far it has read.
type Chains interface {
	Words() (networks, assets map[string]string)
}

// The words that say nobody here can do anything about a network: a provider
// that does not answer, one whose rounds have stopped finishing, one that is
// not the chain the document names, and one that will not say which block is
// final. A deployment where every network is one of these sees no payment
// arrive, and is taken out of service.
//
// The words a network can be in besides these leave it in service. The chain
// having moved under the position, no round having finished yet, and a
// provider that has not caught up are all states somebody has to act on, and
// the one who acts is the operator or the provider rather than whatever reads
// this.
//
// Written here rather than taken from what produces them, so that this package
// depends on no particular reader. A test holds this list against the words a
// reader can produce, which is what keeps the two from parting.
var unread = []string{"unreachable", "stalled", "chain-mismatch", "no-finalized"}

// serving reports whether any network is one a payment could still be seen
// arriving on. A deployment reading none at all is serving: every build before
// there was anything to read was.
func serving(networks map[string]string) bool {
	if len(networks) == 0 {
		return true
	}
	for _, word := range networks {
		if !slices.Contains(unread, word) {
			return true
		}
	}
	return false
}

// words are what the deployment's networks and assets have come to, and
// nothing where nothing reads a chain.
func words(chains Chains) (networks, assets map[string]string) {
	if chains == nil {
		return nil, nil
	}
	return chains.Words()
}
