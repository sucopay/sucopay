# Contributing

日本語: [CONTRIBUTING.ja.md](CONTRIBUTING.ja.md)

## Getting set up

```bash
make dev        # run the database in Docker
make test
make check      # everything CI runs
```

Requires Go 1.26+. `make dev` needs Docker, and `make check` needs golangci-lint.

The tests need that database. They do not skip without it: a test that skips reports
success without having run. `SUCO_TEST_DATABASE_URL` names it, and the Makefile sets it to
what `make dev` starts.

## Code, tests, commits, comments

Each answers a different question. Put the answer where it belongs.

| | Answers |
|---|---|
| Code | How |
| Tests | What |
| Commit messages | Why |
| Comments | Why not |

## Architecture

Backend and CLI both follow the dependency rule from Clean Architecture: inner code does not
reference outer code. Domain types do not import a repository implementation, an HTTP handler,
or a chain adapter.

Split domain packages by bounded context. Split layers by file inside them.

```
internal/payment/
  payment.go      aggregate, value objects, invariants
  service.go      use cases
  repository.go   repository interface
  postgres.go     repository implementation
  http.go         handlers
```

- Do not create packages named `domain`, `application`, `infrastructure`, `common`, `utils`
  or `helpers`. A package name says what it carries, never which layer it belongs to.
- A package that serves the whole process rather than one context is named for its concern.
  `config` is one.
- `api` holds the server and the routing that mounts contexts. A context's own handlers live
  with it, in `internal/<context>/http.go`.
- Declare interfaces in the package that calls them, next to the caller. An interface that
  more than one caller uses and every adapter implements goes in a package of its own,
  where all of them can import it. `internal/adapter/chain` is one.
- Wire dependencies in `main` only.
- Keep a context's internals inside it. Cross-context work goes through domain events or an
  exported service, never through another context's repository.

Check the direction with a test that reads a package's imports and fails on anything the
inner files are not allowed. `internal/accepted`, `internal/adapter/chain`,
`internal/adapter/chain/evm`, `internal/config`, `internal/credential`, `internal/finality`,
`internal/observe`, `internal/payment` and `internal/postgres` each have one.

## Domain model

Domain-Driven Design (DDD) concepts map onto suco Pay as follows.

| DDD | suco Pay |
|---|---|
| Aggregate root | `Payment`, `Attempt` (one go at paying a payment, pointing to it), `Refund` |
| Entity | `Payment`, `Refund`, `Transaction`, `Attempt` |
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

## Defaults

Every setting has a default that works for the common case.

- Require a setting only where no default can be right. A destination wallet has no sensible
  default; a confirmation policy does.
- Write the values a command chose into the document it generates, including the ones that match
  the built-in default.
- Ship the command that produces anything you require. `suco serve` requires `suco.yaml`, so
  `suco init` writes one.

## Settings

A setting is named for the value it holds.

- The top level names what is being configured: `listen`, `log`, `database`, `credentials`,
  `networks`, `assets`. Under `networks` and `assets`, the level below is a name the document
  chooses.
- A leaf is a noun, or for a switch the word that describes what it turns on: `port`, `kind`,
  `width`, `poll`, `managed`. Not a phrase with a verb in it. A name like `give_up_after` reads
  as a length of time and holds a count.
- Words are joined with an underscore: `base_url`, `chain_id`, `key_id`.
- A leaf carries no unit. The value carries it, and the configuration reference says which:
  `poll` is `12s`, `width` is a number of blocks.
- Settings that belong to one decision are grouped under a noun, as `rpc.own` and `rpc.others`
  are. Group them when a reader would otherwise have to know that leaves sitting apart are about
  the same thing.
- Document a new setting in `docs/configuration.md` and `docs/configuration.ja.md` in the same
  change. Nothing checks that the tables and the code agree, so a setting left out of one of them
  is one half the readers cannot find.

## Errors

An error repeats nothing typed after `suco`. The word may be a token, from the file
`credential new` wrote, when a script has lost the word before it; and an error is what a CI
log keeps. Say what the command takes, and count what was surplus.

```
credential new takes --read-only or --read-write, got neither
serve takes no arguments, got 1
```

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

- Break what a test is named for and watch it fail before you keep it. A name is a claim, and
  a test that passes either way makes the claim without checking it.
- Table-driven tests with `t.Run` per case. `t.Helper()` in assertion helpers.
- Every state transition needs a test for its failure path.
- Anything scoped by account needs a test with two accounts asserting isolation.
- Test behaviour through the package's exported surface, not through unexported state.
- `t.Parallel()` in every test that does not set an environment variable, so that a test
  depending on another one's leftovers fails rather than passes quietly.
- A test needing a database gets one of its own from `postgrestest.Fresh`, and does not skip
  when there is none: a skipped test reports success without having run. A test that reads a
  chain somebody else runs is the exception, because nothing here can start one. It takes the
  endpoint from the environment and skips when none is named.

### Fuzzing

Anything that reads what somebody else wrote gets a fuzz target: a configuration document, an
amount, an address, metadata. Write the invariant rather than the expected output, because most
generated inputs are refused and being refused is not a failure.

```go
// Never panics, and an error never repeats what it was given.
// Nothing that reaches a terminal holds a character a terminal would act on.
```

**Write the check without going through the function under test.** Asking `Has`
whether `Quote` did its job routes both through one decision, so a character
dropped from that decision is missed by both at once, however long it runs.

The same trap has a second shape: `slog`'s text handler escapes anything it
thinks unprintable, so a test reading text output passes whether or not the
value was quoted first. Its JSON handler escapes only what JSON requires and
passes a zero-width space or a bidi override through as given, which is the
format a deployment writes. Check quoting against JSON.

`go test` runs each target's seed corpus, so a case found once stays checked. `make fuzz`
searches for longer. When a target fails, Go writes the input under `testdata/fuzz/`: commit it.

## Commits and PRs

Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`). One logical
change per PR. Run `make check` before you commit; `make hooks` installs a
hook that refuses a commit of anything `make check` has not seen.

Write for someone with the repository and nothing else. A message that cites a document they
cannot open tells them a reason exists and withholds it.

The diff already says what changed. Use the body for the motivation, the alternatives you
rejected, and the trade-off you accepted.

```
fix: hold the outbox write inside the payment transaction

The webhook could report payment.succeeded for a payment whose update later
rolled back, so a merchant saw a payment we did not have.

Considered publishing after commit and reconciling the gap. Rejected: the
window is unbounded when the process dies between the two writes.
```

## Before you open a PR

- Open an issue first for anything that changes the API, the payment state machine, or adds a
  dependency to the core.
- Run `make check`.

## Documentation

Conventions for writing docs are in [WRITING-STYLE.ja.md](WRITING-STYLE.ja.md) (Japanese).
They apply to the English docs as well.

## License

Contributions are licensed under [Apache-2.0](LICENSE), the same as the project. There is no
Contributor License Agreement (CLA).

## Security

Don't open a public issue. See [SECURITY.md](SECURITY.md).
