<h1 align="center">suco Pay</h1>

<p align="center">
  <b>Open-source payment infrastructure for stablecoins</b>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/discussions">Discussions</a> ·
  <a href="README.ja.md">日本語</a>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/actions/workflows/ci.yml"><img src="https://github.com/sucopay/sucopay/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/status-pre--alpha-orange" alt="Status: pre-alpha">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache-2.0"></a>
</p>

---

Create a payment, get paid on chain, know when it's final, refund it, and get a webhook.
suco Pay runs on your own infrastructure. Funds go straight from the customer's wallet to yours.

> **Pre-alpha.** Nothing works yet. Watch the repository or join
> [Discussions](https://github.com/sucopay/sucopay/discussions).

- [ ] Payments: lifecycle, expiry, under/overpayment, idempotency
- [ ] Finality: confirmation policy per network
- [ ] Checkout: hosted and embeddable
- [ ] Webhooks: signed, retried, with delivery history
- [ ] Refunds: records and transfer intents
- [ ] Reconciliation: periodic diff between internal state and the chain
- [ ] Console: payments, transactions, refunds, webhook deliveries
- [ ] CLI: `init` `dev` `listen` `migrate` `doctor` `upgrade`

## Getting started

```bash
curl -fsSL https://get.sucopay.dev | sh
suco init
cd suco && suco dev
```

The console is served at `http://localhost:7826`. Create your first payment there.

```bash
curl -X POST http://localhost:7826/v1/payments \
  -H 'Authorization: Bearer sk_test_...' \
  -d '{"amount":"1000","asset":"JPYC","network":"polygon"}'

suco listen --forward http://localhost:3000/webhooks
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[SECURITY.md](SECURITY.md) instead.

## License

[Apache-2.0](LICENSE)
