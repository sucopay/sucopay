package chain

// Kind is one kind of chain as the process registers it: the name a
// configuration document declares, and what the process needs from the kind
// before it has an adapter.
type Kind struct {
	// Name is what a network's configuration document declares as its kind.
	Name string
	// Normalize puts a reference to an asset or an account into the one form
	// the kind compares in, and refuses what is not a reference on this kind
	// of chain.
	Normalize func(reference string) (string, error)
	// Open makes the adapter for one network, and refuses settings the kind
	// cannot use.
	Open func(Settings) (Chain, error)
}

// Settings is what an adapter is opened with. A kind leaves empty what it has
// no use for.
type Settings struct {
	// Name is the network's name in the configuration document.
	Name string
	// ChainID is the identity the chain is expected to have, written the way
	// [Chain.Identity] writes it.
	ChainID string
	// RPC is where the chain is reached. It may carry a credential, and
	// nothing that reads it writes it anywhere.
	RPC string
}
