// Command suco is the suco Pay server and CLI.
//
//	suco serve     run the server: HTTP API, chain observer, webhooks, reconciliation
//	suco init      create a project and a runnable environment
//	suco dev       run the server locally, with .env loaded and a managed database
//	suco listen    stream events and forward them to a local endpoint
//	suco migrate   apply database migrations
//	suco doctor    report the effective configuration and check connectivity
//	suco upgrade   check, migrate and update to a newer version
//
// The CLI orchestrates; it is not a required control plane. Everything it does
// can also be done by operating the container image and configuration directly.
package main

func main() {}
