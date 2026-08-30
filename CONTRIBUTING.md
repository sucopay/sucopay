# Contributing

日本語: [CONTRIBUTING.ja.md](CONTRIBUTING.ja.md)

## Getting set up

```bash
make dev        # start the development stack
make test
make lint
```

Requires Go 1.26+ and Docker. The web packages additionally require Node 24, the current
Active LTS.

## Code, tests, commits, comments

Each answers a different question. Put the answer where it belongs.

| | Answers |
|---|---|
| Code | How |
| Tests | What |
| Commit messages | Why |
| Comments | Why not |

Code says how the machine does it. Tests say what the software promises. Commit messages say
why the change was made. Comments say why the obvious version was rejected.

## Architecture

Backend and CLI both follow the dependency rule from Clean Architecture: inner code does not
reference outer code. Domain types do not import a repository implementation, an HTTP handler,
or a chain adapter.

Split packages by bounded context. Split layers by file inside them.

```
internal/payment/
  payment.go      aggregate, value objects, invariants
  service.go      use cases
  repository.go   repository interface
  postgres.go     repository implementation
  http.go         handlers
```

- Do not create packages named `domain`, `application`, `infrastructure`, `common`, `utils`
  or `helpers`.
- Declare interfaces in the package that calls them, next to the caller.
- Wire dependencies in `main` only.
- Keep a context's internals inside it. Cross-context work goes through domain events or an
  exported service, never through another context's repository.

A test verifies the import direction.

## Domain model

| DDD | suco Pay |
|---|---|
| Aggregate root | `Payment` (owns its attempts), `Refund` |
| Entity | `Payment`, `Refund`, `Transaction` |
| Value object | `Money`, `Address`, `Network`, `Status`, `Nonce`, `ConfirmationPolicy` |
| Repository | one per aggregate root |
| Domain service | finality evaluation |
| Application service | create payment, observe transaction, deliver webhook |
| Domain event | `payment.succeeded`, written to the outbox in the same transaction |

- Invariants live on the aggregate. A constructor must not be able to return an invalid
  `Payment`.
- State transitions are methods on the aggregate. A `switch` on status inside a service means
  the transition belongs on the aggregate.
- One database transaction changes one aggregate.
- Only aggregate roots get repositories.

### Money

`Money` is a value object holding an integer amount in the asset's smallest unit, plus the
asset.

- Never use `float32` or `float64` for an amount.
- Never add or compare `Money` values of different assets.
- JPYC has 18 decimals. Amounts do not fit in `int64`.

## Comments

Follow the [Go doc comment conventions](https://go.dev/doc/comment). Every exported name gets a
doc comment that starts with the name and is a complete sentence. Document the zero value where
it is not obvious, and concurrency safety where it differs from the default.

Inside a function, write a comment when a reader would reasonably expect different code. Name
the rejected approach.

```go
// Not sql.LevelSerializable: the observer re-reads the same rows every tick,
// and serialisation failures cost more than the version check they replace.
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
```

```go
// Not comparing with ==: callers send JPYC amounts with trailing zeros.
if got.Cmp(want) != 0 {
```

Reviewers delete comments that restate the code. If the code needs a comment to be readable,
change the code.

Use `TODO(username):` for work left undone.

## Tests

Name a test so that reading the test names of a package tells you what the package promises.

```go
func TestPayment_ExpiresOnlyFromAwaiting(t *testing.T)
func TestPayment_ConcurrentUpdatesLoseOnStaleVersion(t *testing.T)
func TestObserver_SameTransactionObservedTwiceCreatesOneRow(t *testing.T)
```

- Table-driven tests with `t.Run` per case. `t.Helper()` in assertion helpers.
- Every state transition needs a test for its failure path.
- Anything scoped by account needs a test with two accounts asserting isolation.
- Test behaviour through the package's exported surface, not through unexported state.

## Commits and PRs

Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`). One logical
change per PR.

The diff already says what changed. Use the body for the motivation, the alternatives you
rejected, and the trade-off you accepted.

```
fix: hold the outbox write inside the payment transaction

Delivery could report payment.succeeded for a payment whose update later
rolled back, so a merchant saw a payment we did not have.

Considered publishing after commit and reconciling the gap. Rejected: the
window is unbounded when the process dies between the two writes.
```

## Before you open a PR

- Open an issue first for anything that changes the API, the payment state machine, or adds a
  dependency to the core.
- Run `make lint test`.

## Documentation

Conventions for writing docs are in [WRITING-STYLE.ja.md](WRITING-STYLE.ja.md) (Japanese).
They apply to the English docs as well.

## License

Contributions are licensed under [Apache-2.0](LICENSE), the same as the project. There is no
CLA.

## Security

Don't open a public issue. See [SECURITY.md](SECURITY.md).
