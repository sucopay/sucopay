package evm

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/sha3"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/invisible"
)

// domainSeparatorSelector is the first four bytes of the keccak-256 of
// "DOMAIN_SEPARATOR()", which is how the function is called.
const domainSeparatorSelector = "0x3644e515"

// domainType is the EIP-712 type the domain is hashed as.
const domainType = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"

// nameSelector is the first four bytes of the keccak-256 of "name()", which
// is how ERC-20 has a contract asked what it calls itself.
const nameSelector = "0x06fdde03"

// maxName is the most bytes a name may hold. A name is repeated beside a
// document's, and the bound on a body alone would let a provider answer with
// megabytes of one.
const maxName = 256

// maxStringBytes is the longest answer a string of maxName bytes takes: the
// two words before it, and the bytes padded to a whole word.
const maxStringBytes = 2*wordBytes + (maxName+wordBytes-1)/wordBytes*wordBytes

// DomainSeparator reads what the asset's contract signs under, at the
// newest block. A contract that answers nothing has no code, and one that
// reverts with nothing to say has no DOMAIN_SEPARATOR(): the first is
// refused, and the second answers [chain.ErrNoSeparator], for the caller to
// check what the contract does answer.
func (n *network) DomainSeparator(ctx context.Context, asset string) ([32]byte, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return [32]byte{}, err
	}
	var raw json.RawMessage
	if err := n.client.call(ctx, "eth_call", []any{
		map[string]string{"to": contract.String(), "data": domainSeparatorSelector}, "latest",
	}, &raw); err != nil {
		if errors.Is(err, errReverted) {
			return [32]byte{}, fmt.Errorf("the contract at %s: %w", asset, chain.ErrNoSeparator)
		}
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

// Name reads what the contract calls itself, at the newest block. The answer
// is an ABI string: a word saying where the bytes begin, a word saying how
// many there are, and the bytes padded out to whole words.
func (n *network) Name(ctx context.Context, asset string) (string, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return "", err
	}
	var raw json.RawMessage
	if err := n.client.call(ctx, "eth_call", []any{
		map[string]string{"to": contract.String(), "data": nameSelector}, "latest",
	}, &raw); err != nil {
		return "", err
	}
	if string(raw) == `"0x"` {
		return "", fmt.Errorf("the contract at %s answers no name()", asset)
	}
	name, err := decodeString(raw)
	if err != nil {
		// Through the client, as the separator's errors go: the answer is
		// the provider's writing.
		return "", n.client.failed("name()", err)
	}
	return name, nil
}

// decodeString reads the one ABI string a function answers. The layout is
// held to exactly: the offset is the width of the one word before the
// length, the length is how many bytes follow, what follows pads out to a
// whole word with zeros, and neither number has anything above its last
// eight bytes. The name is then held to what a reader can see whole: UTF-8,
// no character that does not show, and no more than maxName bytes.
func decodeString(raw json.RawMessage) (string, error) {
	text, err := unquote(raw)
	if err != nil {
		return "", err
	}
	digits, found := strings.CutPrefix(text, "0x")
	if !found {
		return "", fmt.Errorf("%q does not start with 0x", cut(text))
	}
	if len(digits) > 2*maxStringBytes {
		return "", fmt.Errorf("%d digits, more than a name of %d bytes takes", len(digits), maxName)
	}
	payload, err := hex.DecodeString(digits)
	if err != nil {
		return "", fmt.Errorf("%q is not hexadecimal", cut(text))
	}
	if len(payload) < 2*wordBytes {
		return "", fmt.Errorf("%d bytes, fewer than the two words a string begins with", len(payload))
	}
	offset, ok := small(payload[:wordBytes])
	if !ok || offset != wordBytes {
		return "", errors.New("the string does not begin after the first word")
	}
	length, ok := small(payload[wordBytes : 2*wordBytes])
	if !ok || length > maxName {
		return "", fmt.Errorf("a name of more than %d bytes", maxName)
	}
	held := payload[2*wordBytes:]
	padded := (int(length) + wordBytes - 1) / wordBytes * wordBytes
	if len(held) != padded {
		return "", fmt.Errorf("%d bytes after the length, want %d for a string of %d", len(held), padded, length)
	}
	for _, b := range held[length:] {
		if b != 0 {
			return "", errors.New("the padding after the string is not zeros")
		}
	}
	name := string(held[:length])
	if !utf8.ValidString(name) {
		return "", errors.New("the name is not UTF-8")
	}
	if invisible.Has(name) {
		return "", errors.New("the name holds characters that do not show")
	}
	return name, nil
}

// small reads a word as the offset or length of a string: a number with
// nothing above its last eight bytes. Reading only the tail would cut a word
// with more to something plausible.
func small(w []byte) (uint64, bool) {
	for _, b := range w[:wordBytes-8] {
		if b != 0 {
			return 0, false
		}
	}
	return binary.BigEndian.Uint64(w[wordBytes-8:]), true
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
