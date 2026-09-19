package evm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/sha3"
)

// domainSeparatorSelector is the first four bytes of the keccak-256 of
// "DOMAIN_SEPARATOR()", which is how the function is called.
const domainSeparatorSelector = "0x3644e515"

// domainType is the EIP-712 type the domain is hashed as.
const domainType = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"

// DomainSeparator reads what the asset's contract signs under, at the
// newest block. A contract that answers nothing has no DOMAIN_SEPARATOR(),
// and is one no wallet signs a transfer of, so the call is refused rather
// than answered with nothing.
func (n *network) DomainSeparator(ctx context.Context, asset string) ([32]byte, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return [32]byte{}, err
	}
	var raw json.RawMessage
	if err := n.client.call(ctx, "eth_call", []any{
		map[string]string{"to": contract.String(), "data": domainSeparatorSelector}, "latest",
	}, &raw); err != nil {
		return [32]byte{}, err
	}
	if string(raw) == `"0x"` {
		return [32]byte{}, fmt.Errorf("the contract at %s answers no DOMAIN_SEPARATOR(), so a transfer of it cannot be signed", asset)
	}
	var answer word
	if err := json.Unmarshal(raw, &answer); err != nil {
		// Through the client, so that a provider's writing in the answer is
		// cut short and has the endpoint taken out, as every other error
		// that repeats one does.
		return [32]byte{}, n.client.failed("DOMAIN_SEPARATOR()", err)
	}
	return answer, nil
}

// Domain is the EIP-712 domain separator of a contract at contract on the
// chain chainID, deployed under name and version: what [DomainSeparator]
// answers when the document's name and version are the contract's.
func Domain(name, version string, chainID uint64, contract string) ([32]byte, error) {
	at, err := parseAddress(contract)
	if err != nil {
		return [32]byte{}, err
	}
	var encoded []byte
	encoded = append(encoded, keccak256([]byte(domainType))...)
	encoded = append(encoded, keccak256([]byte(name))...)
	encoded = append(encoded, keccak256([]byte(version))...)
	var id word
	binary.BigEndian.PutUint64(id[wordBytes-8:], chainID)
	encoded = append(encoded, id[:]...)
	var where word
	copy(where[wordBytes-addressBytes:], at[:])
	encoded = append(encoded, where[:]...)
	var out [32]byte
	copy(out[:], keccak256(encoded))
	return out, nil
}

// keccak256 is the hash the chain uses, which is not the SHA-3 the standard
// settled on.
func keccak256(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}
