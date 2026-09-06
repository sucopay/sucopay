// Package chain is the boundary between the observer and a chain: the
// interface the observer reads a network through, the values that cross it,
// and the type a kind of chain is registered as.
//
// Nothing here knows a chain. A height is an integer, and a hash, a
// transaction and an account are strings the adapter behind the interface
// gave, handed back to it unread. What the strings look like is the adapter's
// business, and the observer compares them and stores them.
package chain
