package evm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// The first four bytes of the keccak-256 of "paused()" and of
// "isBlocklisted(address)": how the two controls an issuer holds are read. Both
// answer on JPYC, which is what they were read against.
const (
	pausedSelector      = "0x5c975abb"
	blocklistedSelector = "0x8e204c43"
)

// Paused reads whether the contract has stopped every transfer, at the newest
// block.
func (n *network) Paused(ctx context.Context, asset string) (bool, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return false, err
	}
	return n.flag(ctx, contract, pausedSelector)
}

// Blocklisted reads whether the contract refuses transfers from or to account,
// at the newest block. The account follows the selector as one word with the
// address in its last twenty bytes, which is how the ABI lays an address out.
func (n *network) Blocklisted(ctx context.Context, asset, account string) (bool, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return false, err
	}
	who, err := parseAddress(account)
	if err != nil {
		return false, err
	}
	var arg word
	copy(arg[wordBytes-addressBytes:], who[:])
	return n.flag(ctx, contract, blocklistedSelector+hex.EncodeToString(arg[:]))
}

// flag calls a function of a contract that answers one boolean, and reads the
// word it answers as one.
//
// Only a word of zero or one is a flag. An empty answer is what an address
// with no code gives, and what a contract whose fallback returns nothing
// gives; a contract without the function reverts, which comes back as the
// provider's own error. None of those is false. A caller that took "nothing"
// for "not paused" would let a provider answering nothing switch the check
// off, and the check is there for the provider not to decide.
func (n *network) flag(ctx context.Context, contract address, data string) (bool, error) {
	var raw json.RawMessage
	if err := n.client.call(ctx, "eth_call", []any{
		map[string]string{"to": contract.String(), "data": data}, "latest",
	}, &raw); err != nil {
		return false, err
	}
	if string(raw) == `"0x"` {
		return false, fmt.Errorf("eth_call: the contract at %s answered nothing", contract)
	}
	var answer word
	if err := json.Unmarshal(raw, &answer); err != nil {
		return false, n.client.failed("eth_call", err)
	}
	for _, b := range answer[:wordBytes-1] {
		if b != 0 {
			return false, fmt.Errorf("eth_call: the contract at %s answered %s, which is not a flag", contract, answer)
		}
	}
	switch answer[wordBytes-1] {
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	return false, fmt.Errorf("eth_call: the contract at %s answered %s, which is not a flag", contract, answer)
}
