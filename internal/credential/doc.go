// Package credential mints the bearer tokens an API request is authenticated
// with, and computes the form one is stored in.
//
// A token is 32 bytes from crypto/rand. What is stored is its HMAC-SHA256
// under a key the deployment holds, so a table read on its own presents
// nothing, and cannot be checked against a guess without the key.
package credential
