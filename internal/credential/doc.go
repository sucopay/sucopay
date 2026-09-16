// Package credential mints the bearer tokens an API request is authenticated
// with, and keeps them: the form one is stored in, the account and access
// one stands for, and the store that finds one by the token presented.
//
// A token is 32 bytes from crypto/rand. What is stored is its HMAC-SHA256
// under a key the deployment holds, so a table read on its own presents
// nothing, and cannot be checked against a guess without the key.
package credential
