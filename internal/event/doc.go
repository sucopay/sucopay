// Package event defines domain events and the transactional outbox they are
// written to.
//
// A state change and the event it produces commit in one transaction. Delivery
// happens separately and is at-least-once, so consumers must be idempotent.
package event
