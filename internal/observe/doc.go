// Package observe holds what one instance needs to read a chain: how far each
// network has been read, and which instance is reading it.
//
// The position and the records found at it are written in one transaction, so
// [Cursors.Advance] takes the caller's transaction rather than opening one of
// its own. Everything else here stands alone and bounds its own call.
package observe
