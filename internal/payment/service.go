package payment

import (
	"context"
	"errors"
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
// The clock comes in rather than being read from the package. A payment's
// deadline is decided against a moment, and whoever cannot choose that moment
// has to wait for it to arrive.
func NewService(payments Repository, now func() time.Time) *Service {
	return &Service{payments: payments, now: now}
}

// DefaultExpiry is how long a payment stays payable when the request names no
// deadline. It is one value for every account.
const DefaultExpiry = time.Hour

// MaxExpiry is the furthest off a request may put a deadline. It too is one
// value for every account.
const MaxExpiry = 30 * 24 * time.Hour

// Open records that an amount is being accepted for a piece of business.
//
// The payment is not payable yet. Whatever shows it to a customer calls
// [Service.Await] when it is ready to, so that a payment cannot be paid before
// whoever asked for it has finished setting it up.
//
// A request naming no deadline gets one [DefaultExpiry] from now, and one
// naming a deadline further off than [MaxExpiry] is refused along with
// whatever else is wrong with it. The bound is read here rather than by [New],
// which [Restore] shares: a payment stored under a bound that has since been
// lowered still has to load.
func (s *Service) Open(ctx context.Context, account AccountID, r Request) (*Payment, error) {
	now := s.now()
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = now.Add(DefaultExpiry)
	}
	p, err := New(r, now)
	var problems Problems
	if err != nil && !errors.As(err, &problems) {
		return nil, err
	}
	if latest := now.Add(MaxExpiry); r.ExpiresAt.After(latest) {
		problems = append(problems, Problem{Field: "expires_at", Message: fmt.Sprintf(
			"%s is after %s, the latest deadline a payment opened now can have",
			r.ExpiresAt.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339))})
	}
	if len(problems) > 0 {
		return nil, problems
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
