# Security policy

## Reporting a vulnerability

Report privately through GitHub's [private vulnerability
reporting](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing/privately-reporting-a-security-vulnerability).

Please don't open a public issue. We'll acknowledge within 3 business days and keep you updated
as we work on a fix. Credit in the advisory if you want it.

## Scope

Reports we most want:

- anything that lets a payment be marked succeeded without the funds having moved
- forged or replayable webhooks
- API authentication or authorization bypass
- private key or secret exposure
- idempotency failures leading to double processing
- reorg or confirmation-policy handling that can be gamed

Out of scope: findings against a deployment's own misconfiguration, and anything requiring
control of the host.

## Supported versions

Pre-alpha. Only `main` is supported.
