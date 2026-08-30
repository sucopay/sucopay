// Package refund handles refunds and their lifecycle.
//
// A refund produces a transfer intent. The core does not sign or broadcast it;
// it observes the resulting transaction.
package refund
