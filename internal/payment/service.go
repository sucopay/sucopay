package payment

import (
	"context"
	"fmt"
	"time"
)

// Service is what a request handler or a worker calls. It holds no state of its
// own: everything it decides from is either passed in or read back out of the
// repository, so two instances running it are the same instance.
type Service struct {
	payments Repository
	now      func() time.Time
}

// NewService returns a service over payments, reading the time from now.
//
// The clock is passed in rather than read from the package, for the reason the
// aggregate takes a time rather than calling time.Now: a payment's deadline is
// decided against a moment, and a test that cannot choose that moment has to
// wait for it.
func NewService(payments Repository, now func() time.Time) *Service {
	return &Service{payments: payments, now: now}
}

// Open records that an amount is being accepted for a piece of business.
//
// The payment is not payable yet. Whatever shows it to a customer calls
// [Service.Await] when it is ready to, so that a payment cannot be paid before
// whoever asked for it has finished setting it up.
func (s *Service) Open(ctx context.Context, account AccountID, r Request) (*Payment, error) {
	p, err := New(r, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.payments.Create(ctx, account, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Await makes a payment payable.
//
// It reports [ErrStale] rather than trying again, because who should try again
// is not the same answer everywhere: a worker reads the payment and decides
// once more, and a request tells its caller the payment moved underneath it.
func (s *Service) Await(ctx context.Context, account AccountID, id ID) (*Payment, error) {
	p, at, err := s.payments.Find(ctx, account, id)
	if err != nil {
		return nil, err
	}
	if err := p.Await(); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if err := s.payments.Save(ctx, account, p, at); err != nil {
		return nil, err
	}
	return p, nil
}
