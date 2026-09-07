# Hubble first-increment verification — 2026-09-07

Base: master at 69312cf2d18db4826fe3677dff237d76f254f31a (logging PR #4).

## Automated checks

Passed locally with Go 1.25.8:

- `go test ./internal/view ./internal/hubble ./internal/config/...`
- `go test -race ./internal/hubble`
- `go build .`
- `git diff --check`
- `golangci-lint v2.6.2 run --new-from-rev=origin/master` on Hubble, view and config packages: zero new issues.

Coverage includes immutable bounded snapshots under 100,000 incoming events,
frozen conversation/detail selection, malformed-filter preservation, symmetric
server filters and exact cluster/pod scope, non-pod identity, safe L7 projection,
evidence-only policy attribution, real gRPC streaming, cancellation/disconnect,
reported event loss, mutual TLS, bad server names and missing client certificates.
Help-mode restoration and repeated-stop cancellation have regression tests.

## Separate private iximiuz lab

[Hubble test lab](https://labs.iximiuz.com/playgrounds/k9plus-hubble-015843a9/6a9ea668cf75e1c616221180)
uses Cilium and Relay 1.20.1 on three nodes. The existing logging lab was inspected
read-only; it had no Cilium/Hubble resources and its networking was preserved.

The opt-in native Relay integration test passed:

| Scenario | Observed result |
| --- | --- |
| Allowed client to nginx | FORWARDED TCP events |
| Denied client to nginx | DROPPED with POLICY_DENIED |
| External peer | world 1.1.1.1, FORWARDED TCP |
| Missing L7 | No L7 records; visibility labeled unknown |
| Coverage | Three connected nodes, zero unavailable |
| Server filters | Dropped/IP predicates returned matching events |

Manual tmux TUI checks exercised Relay status/help restoration, Deployment scope resolution, peers -> conversation -> detail, destination
pod jump, dropped-only filtering, rejected malformed replacement, disconnect by
stopping the test Relay port-forward, and explicit reconnect after restoring it.
A 2,500-request nginx burst produced more than 41,000 local evictions while the
frozen event body remained byte-for-byte unchanged. Local eviction and Relay loss
were shown separately. No Relay loss occurred naturally; reported-loss handling
was verified using the real gRPC test server's synthetic loss notification.

## Limits

TLS/mTLS and node-status failure cases are controlled gRPC tests, not a Relay
certificate-rotation or multi-version acceptance matrix. Real-lab L7 was not
enabled; FQDN/apiserver identity and policy-name attribution use protobuf fixtures.
History/live handoff is not gapless. No raw export, recording, policy mutation,
DNS analysis or automatic L7 enablement is included. See [usage](hubble.md).
