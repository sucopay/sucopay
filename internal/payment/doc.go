// Package payment records that an amount is being accepted for a piece of
// business, and what has happened to it since.
//
// A payment is the entity, not the transfer that settles it. One payment can
// be attempted more than once, and a chain is one way of fulfilling it rather
// than the thing being tracked.
package payment
