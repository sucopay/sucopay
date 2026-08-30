# Contributing

## Getting set up

```bash
make dev        # start the development stack
make test
make lint
```

Requires Go 1.23+, Docker, and Node 20+ for the web packages.

## Before you open a PR

- Open an issue first for anything that changes the API, the payment state machine, or adds a
  dependency to the core.
- Behaviour changes need tests. Payment state transitions need tests that cover the failure path.
- Run `make lint test`.

## Commits and PRs

Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`). One logical
change per PR. Describe what breaks if the change is wrong.

## Documentation

Conventions for writing docs are in [WRITING-STYLE.ja.md](WRITING-STYLE.ja.md) (Japanese).
They apply to the English docs as well.

## License

Contributions are licensed under [Apache-2.0](LICENSE), the same as the project. There is no
CLA.

## Security

Don't open a public issue. See [SECURITY.md](SECURITY.md).
