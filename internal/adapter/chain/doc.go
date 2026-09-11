// Package chain is the boundary between this project and a chain: the
// interface a network is read through, the values that cross it, and the type
// a kind of chain is registered as.
//
// Nothing here knows a chain. A height is an integer, and a hash, a
// transaction and an account are strings the adapter behind the interface
// gave, handed back to it unread. What the strings look like is the adapter's
// business, and whoever reads a chain compares them and stores them.
package chain
