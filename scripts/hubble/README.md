# Native Hubble integration checks

Use only a disposable Cilium cluster. `fixtures.yaml` creates namespace
`hubble-test`, two curl clients and an nginx server. A namespaced NetworkPolicy
allows only the `allowed` client to reach nginx. The `allowed` client also makes
HTTP requests to 1.1.1.1. No L7 rules or visibility annotations are added.

```sh
kubectl apply -f scripts/hubble/fixtures.yaml
kubectl -n kube-system port-forward svc/hubble-relay 4245:80
# In another terminal:
K9PLUS_HUBBLE_TEST_ADDRESS=127.0.0.1:4245 go test -v ./internal/hubble
```

The test checks allowed, dropped, external events and missing L7. Regular tests
cover mutual TLS, rejected server names/client credentials, lost-event messages,
disconnect/cancellation, immutable snapshots and symmetric filter compilation.
View tests exercise frozen conversation/detail inspection and invalid filters.

Manual TUI checks: select `hubble-test/allowed`, Shift-H, select a peer, Enter,
Enter. Generate traffic while inspecting; the event and scroll must stay fixed.
Esc returns to the conversation, `s` resumes, `1`/`2` jump to pods. Verify
`:cilium`, `/verdict=dropped`, malformed expressions, and stopping the Relay
port-forward. Reconnect with `r` after restoring the port-forward.
