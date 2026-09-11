package evm

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// The events this reads, each written as the hash of its signature, which is
// what a chain indexes a log under.
const (
	// authorizationUsed says a key has been spent. EIP-3009 emits it with the
	// account that signed and the key it spent.
	authorizationUsed = "0x98de503528ee59b575ef0c0a2576a82497bfc029a5685b209e9ec333479b10a5"
	// transferred is the transfer itself, which every token of this kind
	// emits.
	transferred = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	// upgraded says the code behind a proxy has been replaced. It is EIP-1967's,
	// and a token that is not a proxy never emits it.
	upgraded = "0xbc7cd75a20ee27fd9adebab32041f755214dbc6bffa90cc0225b39da2e5c2d3b"
)

// scheme is the word the observer's rules use for a transfer the payer signed
// and somebody else submitted, which is the one this chain's tokens announce
// by spending a key.
const scheme = "eip3009"

// maxLogs is the most a receipt may carry. Pairing looks behind each
// authorisation for a transfer, so the work is the square of what a receipt
// holds: ten thousand logs is eight seconds, which outlives the term of the
// lease the reader holds. The bound on an answer counts what an array holds
// and a receipt is an object, so its logs are counted here instead.
//
// A transaction nobody could pay the gas for is where the line goes, and a
// batch of payments comes to a handful. One that really did carry more would
// be refused every round it was read, and never recorded.
const maxLogs = 1000

// Keys are the keys the assets consumed in a span of blocks, with the
// transaction that consumed each, and the assets whose code changed in that
// span. One request answers both: the two events sit on the same contracts,
// under topics of their own.
func (n *network) Keys(ctx context.Context, first, last uint64, assets []string) (chain.Scan, error) {
	// A span that runs backwards is answered with nothing by some providers
	// and refused by others. Nothing is the dangerous one: it reads as a span
	// with no keys in it, and the observer would move past blocks nobody read.
	if first > last {
		return chain.Scan{}, fmt.Errorf("eth_getLogs: no blocks run from %d to %d", first, last)
	}
	watched, err := addresses(assets)
	if err != nil {
		return chain.Scan{}, err
	}
	// A filter naming no contract is a filter on all of them, which a provider
	// answers with every log the span holds.
	if len(watched) == 0 {
		return chain.Scan{}, nil
	}
	var read []logEntry
	filter := map[string]any{
		"fromBlock": quantity(first),
		"toBlock":   quantity(last),
		"address":   watched,
		"topics":    []any{[]string{authorizationUsed, upgraded}},
	}
	if err := n.client.call(ctx, "eth_getLogs", []any{filter}, &read); err != nil {
		return chain.Scan{}, err
	}
	var scan chain.Scan
	for _, entry := range read {
		// Applying the filter is the provider's part, and what comes back is
		// read as if it had not been. A log from a contract nobody asked about
		// would hand the observer a key no asset here could have spent.
		if !slices.Contains(watched, entry.Address) {
			continue
		}
		switch entry.topic() {
		case authorizationUsed:
			key, err := entry.key()
			if err != nil {
				return chain.Scan{}, fmt.Errorf("eth_getLogs: %w", err)
			}
			scan.Consumed = append(scan.Consumed, chain.Consumed{Key: key, Tx: entry.TxHash.String()})
		case upgraded:
			// An asset whose code was replaced twice in one span changed
			// once as far as anything reading this is concerned.
			if changed := entry.Address.String(); !slices.Contains(scan.Changed, changed) {
				scan.Changed = append(scan.Changed, changed)
			}
		}
	}
	return scan, nil
}

// Receipt is every transfer one transaction carried, in the order it carried
// them.
func (n *network) Receipt(ctx context.Context, tx string) ([]chain.Transfer, error) {
	var id hash
	if err := decodeHex("transaction", tx, id[:]); err != nil {
		return nil, err
	}
	var read *receipt
	if err := n.client.call(ctx, "eth_getTransactionReceipt", []any{id.String()}, &read); err != nil {
		return nil, err
	}
	if read == nil {
		return nil, fmt.Errorf("eth_getTransactionReceipt: no receipt for %s: %w", id, chain.ErrNoTransaction)
	}
	// A receipt is asked for by name, and reading one that names another
	// transaction would put its transfers down under the name asked for.
	if read.TransactionHash != id {
		return nil, fmt.Errorf("eth_getTransactionReceipt: asked for %s and answered for %s", id, read.TransactionHash)
	}
	transfers, err := read.transfers(id.String())
	if err != nil {
		return nil, fmt.Errorf("eth_getTransactionReceipt: %w", err)
	}
	// The block is read for the moment it stamps a transfer with. A receipt
	// with no transfer in it needs none.
	if len(transfers) == 0 {
		return nil, nil
	}
	block, err := n.blockBy(ctx, read.BlockNumber.String())
	if err != nil {
		return nil, err
	}
	// The time a transfer carries is the one the chain stamped on the block it
	// was in, and the block at a height is not always the one that was there.
	// Stamping a transfer from another block would give it a time the chain
	// never gave it.
	if block.Hash != read.BlockHash.String() {
		return nil, fmt.Errorf("eth_getBlockByNumber: %s is at height %s now, and the receipt is from %s",
			block.Hash, read.BlockNumber, read.BlockHash)
	}
	for i := range transfers {
		transfers[i].Block = block
	}
	return transfers, nil
}

// receipt is what a transaction left behind, in the fields this reads.
type receipt struct {
	TransactionHash hash       `json:"transactionHash"`
	BlockHash       hash       `json:"blockHash"`
	BlockNumber     quantity   `json:"blockNumber"`
	Logs            []logEntry `json:"logs"`
}

// transfers are the pairs the receipt carries: an authorisation, and the
// transfer of the same token that follows it.
//
// A pair is made of two logs of one contract, so everything else drops out
// without being named, the chain's own fees included. The same receipt read
// twice gives the same pairs in the same order, because the order is the logs'
// own.
func (r *receipt) transfers(tx string) ([]chain.Transfer, error) {
	if len(r.Logs) > maxLogs {
		return nil, fmt.Errorf("a receipt of %d logs is more than one transaction writes", len(r.Logs))
	}
	var made []chain.Transfer
	// One transfer answers for one authorisation. Two authorisations reading
	// the same transfer would put one payer's payment down against the other's
	// key, which is a payment credited to somebody who never made it.
	taken := make([]bool, len(r.Logs))
	for i, spent := range r.Logs {
		if spent.topic() != authorizationUsed {
			continue
		}
		key, err := spent.key()
		if err != nil {
			return nil, err
		}
		signer, err := spent.indexed(1)
		if err != nil {
			return nil, err
		}
		at, found := followed(r.Logs, taken, i+1, spent.Address)
		if !found {
			continue
		}
		moved := r.Logs[at]
		from, err := moved.indexed(1)
		if err != nil {
			return nil, err
		}
		to, err := moved.indexed(2)
		if err != nil {
			return nil, err
		}
		// What the authorisation authorised is a transfer out of the account
		// that signed it. Another account's transfer, in the same receipt and
		// of the same token, is somebody else's payment.
		if from != signer {
			continue
		}
		value, err := units(moved.Data)
		if err != nil {
			return nil, err
		}
		taken[at] = true
		made = append(made, chain.Transfer{
			Scheme:     scheme,
			Asset:      spent.Address.String(),
			Key:        key,
			Authorizer: signer.String(),
			From:       from.String(),
			To:         to.String(),
			Value:      value,
			Tx:         tx,
			Position:   len(made),
		})
	}
	return made, nil
}

// followed is where the transfer of one contract is that comes after an
// authorisation and answers for no other.
//
// The nearest one: a token spends the key and moves in the same call, so the
// two logs are written together. Where a relay authorised several transfers
// before making any of them, the nearest one is the first that is still going
// spare, which puts the transfers against the authorisations in the order both
// were written in. A relay that made them in some other order would leave an
// authorisation holding a transfer of the same payer that is not the one it
// authorised, or holding none: nothing in a log ties a transfer to the key
// that authorised it, so the order is all there is to go on.
func followed(logs []logEntry, taken []bool, from int, of address) (int, bool) {
	for i := from; i < len(logs); i++ {
		if !taken[i] && logs[i].Address == of && logs[i].topic() == transferred {
			return i, true
		}
	}
	return 0, false
}

// logEntry is one log as a provider answers with one.
type logEntry struct {
	Address address `json:"address"`
	Topics  topics  `json:"topics"`
	// Data is what the log carries unindexed, which for a transfer is the
	// amount. It is kept as written: what sits there depends on the event.
	Data   string `json:"data"`
	TxHash hash   `json:"transactionHash"`
}

// topic is the event the log announces, or nothing where a log carries no
// topic at all.
func (l *logEntry) topic() string {
	if len(l.Topics) == 0 {
		return ""
	}
	return l.Topics[0].String()
}

// key is what an authorisation spent, written the way the observer writes the
// keys it issues: the digits alone, in lower case.
func (l *logEntry) key() (string, error) {
	if len(l.Topics) != 3 {
		return "", fmt.Errorf("an authorisation indexes %d values, want 3", len(l.Topics))
	}
	return hex.EncodeToString(l.Topics[2][:]), nil
}

// indexed is the account one of the log's topics carries.
func (l *logEntry) indexed(at int) (address, error) {
	if at >= len(l.Topics) {
		return address{}, fmt.Errorf("a log indexing %d values has no account at %d", len(l.Topics), at)
	}
	return account(l.Topics[at])
}

// units is the amount a transfer carries: one word, counted in the smallest
// unit of the asset.
func units(data string) (string, error) {
	var carried word
	if err := decodeHex("transfer", data, carried[:]); err != nil {
		return "", err
	}
	return new(big.Int).SetBytes(carried[:]).String(), nil
}

// addresses reads the references of the assets to watch, so that a request
// carries nothing this chain would not call an address.
func addresses(assets []string) ([]address, error) {
	watched := make([]address, 0, len(assets))
	for _, asset := range assets {
		read, err := parseAddress(asset)
		if err != nil {
			return nil, err
		}
		watched = append(watched, read)
	}
	return watched, nil
}
