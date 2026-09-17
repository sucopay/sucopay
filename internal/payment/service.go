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
	payments  Repository
	attempts  Attempts
	positions Positions
	now       func() time.Time
}

// NewService returns a service over payments and the attempts at paying them,
// reading the time from now and asking positions which networks are read.
//
// The clock comes in rather than being read from the package. A payment's
// deadline is decided against a moment, and whoever cannot choose that moment
// has to wait for it to arrive.
func NewService(payments Repository, attempts Attempts, positions Positions, now func() time.Time) *Service {
	return &Service{payments: payments, attempts: attempts, positions: positions, now: now}
}

// DefaultExpiry is how long a payment stays payable when the request names no
// deadline. It is one value for every account.
//
// The deadline is what a payer signs against, and what they sign stands until
// it passes: an authorisation handed over at noon can still be spent at the
// end of it. A request that wants longer asks for it.
const DefaultExpiry = 15 * time.Minute

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
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return s.OpenAs(ctx, account, id, r)
}

// OpenAs is Open under an identifier the caller minted with [NewID]. For a
// caller that has to know the identifier before the payment exists, which
// one deriving the checkout page's token from it is.
func (s *Service) OpenAs(ctx context.Context, account AccountID, id ID, r Request) (*Payment, error) {
	now := s.now()
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = now.Add(DefaultExpiry)
	}
	p, err := build(id, r, Created, now, now)
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
	// An event, although whoever asked for this move holds the answer:
	// the one asking is the operator's command, and later the payer's page,
	// and the merchant is told that the payment can now be paid.
	event, err := Announce(p)
	if err != nil {
		return nil, err
	}
	if err := s.payments.Save(ctx, account, p, at, event); err != nil {
		return nil, err
	}
	return p, nil
}

// Supersede retires the live attempt of a payment, at the payer's word
// that its key was spent by something that did not pay, so that another
// can be issued. Once per payment: a second such word is refused as
// [ErrReissued]. id has to name the live attempt, or [ErrNotFound].
func (s *Service) Supersede(ctx context.Context, account AccountID, payment ID, id AttemptID) error {
	p, _, err := s.payments.Find(ctx, account, payment)
	if err != nil {
		return err
	}
	// Retiring an attempt is for issuing another, and nothing is issued
	// against a payment not open for payment.
	if p.Status() != AwaitingPayment {
		return fmt.Errorf("%s: %w, it is %s", payment, ErrNotAwaiting, p.Status())
	}
	live, at, found, err := s.attempts.Live(ctx, account, payment)
	if err != nil {
		return err
	}
	if !found || live.ID() != id {
		return fmt.Errorf("attempt %s: %w", id, ErrNotFound)
	}
	n, err := s.attempts.Attempted(ctx, account, payment)
	if err != nil {
		return err
	}
	if n >= MaxAttempts {
		return fmt.Errorf("%s: %w", payment, ErrReissued)
	}
	if err := live.Supersede(); err != nil {
		return fmt.Errorf("%s: %w", payment, err)
	}
	return s.attempts.SaveAttempt(ctx, account, live, at)
}

// Find reads one payment back, for whatever shows it. The revision stays
// with the repository: a caller that would move the payment goes through
// [Service.Await], which reads and saves it in one call.
func (s *Service) Find(ctx context.Context, account AccountID, id ID) (*Payment, error) {
	p, _, err := s.payments.Find(ctx, account, id)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Positions reports whether a network has been read. It is what stops a key
// being handed out for a chain nothing has ever watched: the payer would sign,
// the transfer would land, and nobody would ever look.
//
// Position, and not the cursor whoever reads a chain calls it: the question
// here is whether a place has ever been recorded, not what moves.
//
// Ever, and not lately. A reader that has stopped left its cursor where it
// was, and the round that picks the network up again reads forward from there,
// so a transfer that arrived while it was stopped is seen late rather than
// missed. Refusing to issue while a provider has a bad minute would fail a
// checkout for something that rights itself. What a deployment says about a
// network that has stopped being read is a probe's to answer.
type Positions interface {
	// Has reports whether the network is one this deployment has read.
	Has(ctx context.Context, network Network) (bool, error)
}

// ErrNotAwaiting reports that the payment is not open for payment, so nothing
// can be issued against it. A caller that opened the payment calls
// [Service.Await] first.
var ErrNotAwaiting = errors.New("payment: the payment is not awaiting payment")

// NoPosition reports that nothing has ever read the network the payment is on,
// so a transfer against it would never be seen.
type NoPosition struct {
	// Network is the network with no position.
	Network Network
}

// MaxAttempts is how many attempts a payment may have: the first, and one
// reissue. A key spent by something that did not pay is a wallet's
// mistake or a payer's, and one more try settles which without giving
// either anything.
const MaxAttempts = 2

// ErrReissued reports that a payment's one reissue was used.
var ErrReissued = errors.New("payment: the payment was reissued already")

// Error names the network nothing has read.
func (e NoPosition) Error() string { return "payment: no position on " + string(e.Network) }

// Issue gives a payer something to sign: an attempt holding a key that the
// chain lets be spent once. It returns the attempt and the payment it is
// against.
//
// A payment can only be attempted while it is open for payment, while nothing
// else is attempting it, and once its network has been read. The three are
// checked in that order, so a caller is told what the payment is doing before
// it is told about the chain.
func (s *Service) Issue(ctx context.Context, account AccountID, id ID) (*Attempt, *Payment, error) {
	p, _, err := s.payments.Find(ctx, account, id)
	if err != nil {
		return nil, nil, err
	}
	if p.Status() != AwaitingPayment {
		return nil, nil, fmt.Errorf("%s: %w, it is %s", id, ErrNotAwaiting, p.Status())
	}
	_, _, live, err := s.attempts.Live(ctx, account, id)
	if err != nil {
		return nil, nil, err
	}
	if live {
		return nil, nil, fmt.Errorf("%s: %w", id, ErrAttemptLive)
	}
	watched, err := s.positions.Has(ctx, p.Network())
	if err != nil {
		return nil, nil, err
	}
	if !watched {
		return nil, nil, NoPosition{Network: p.Network()}
	}

	a, err := NewAttempt(p, s.now())
	if err != nil {
		return nil, nil, err
	}
	if err := s.attempts.Issue(ctx, account, a); err != nil {
		return nil, nil, err
	}
	return a, p, nil
}
