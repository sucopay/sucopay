package evm

import (
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/sha3"
)

// Normalize writes an account or an asset the one way this compares one: 0x
// and forty lower-case hexadecimal digits.
func Normalize(reference string) (string, error) {
	a, err := parseAddress(reference)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

// parseAddress reads an address written the way this chain's tools write one.
//
// A reference is typed or pasted by somebody, and a wrong digit names an
// account that exists as surely as the right one does. Mixed case is the one
// form that carries a check: EIP-55 decides which letters are raised from the
// hash of the digits, so a digit mistyped in a checksummed address stops
// matching. All of one case carries no check and is taken as written.
func parseAddress(reference string) (address, error) {
	var a address
	if err := decodeHex("address", reference, a[:]); err != nil {
		return address{}, err
	}
	digits := strings.TrimPrefix(reference, "0x")
	lower := strings.ToLower(digits)
	if digits != lower && digits != strings.ToUpper(digits) && digits != raised(lower) {
		return address{}, fmt.Errorf("address: %q is not raised where its checksum says", cut(reference))
	}
	return a, nil
}

// raised writes digits the way EIP-55 does: a letter is upper case where the
// hash of the digits holds 8 or more in the same place.
func raised(lower string) string {
	sum := sha3.NewLegacyKeccak256()
	// A hash takes whatever it is given and has nothing to report about it.
	sum.Write([]byte(lower))
	hashed := hex.EncodeToString(sum.Sum(nil))
	out := []byte(lower)
	for i, c := range out {
		if hashed[i] >= '8' && c >= 'a' && c <= 'f' {
			out[i] = c - 'a' + 'A'
		}
	}
	return string(out)
}

// account reads the address a chain wrote into a word: the twenty low bytes,
// with nothing above them. A word carrying anything higher is not an account,
// and reading one as an account would name whoever the low bytes come to.
func account(w word) (address, error) {
	var a address
	for _, b := range w[:wordBytes-addressBytes] {
		if b != 0 {
			return a, fmt.Errorf("account: %q is not one", cut(w.String()))
		}
	}
	copy(a[:], w[wordBytes-addressBytes:])
	return a, nil
}
