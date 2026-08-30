# Security policy

日本語: [SECURITY.ja.md](SECURITY.ja.md)

## Reporting a vulnerability

Report privately through GitHub's [private vulnerability
reporting](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing/privately-reporting-a-security-vulnerability).
Do not put vulnerability details in an issue, a discussion, or anywhere else public.

## Scope

Reports we most want:

- anything that lets a payment be marked succeeded without the funds having moved
- forged or replayable webhooks
- API authentication or authorization bypass
- private key or secret exposure
- idempotency failures leading to double processing
- reorg or confirmation-policy handling that can be gamed
- the install script, release artifacts, and the container image
- defaults that are unsafe before an operator changes anything
- a dependency or build step that could inject code into a release

Out of scope:

- configuration mistakes in one deployment
- attacks that need existing control of the host, the database, or the wallet keys

## Supported versions

Only `main` receives fixes.
