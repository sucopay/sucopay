// Package observe reads a chain: what one round of it does, how far each
// network has been read, and which instance is reading it.
//
// A round is an [Observer], and there is one for each network. What it reads
// is the boundary every adapter implements, so nothing here is written against
// one kind of chain.
//
// The position and the records found at it are written in one transaction, so
// [Cursors.Advance] takes the caller's transaction rather than opening one of
// its own. Everything else here stands alone and bounds its own call.
package observe
