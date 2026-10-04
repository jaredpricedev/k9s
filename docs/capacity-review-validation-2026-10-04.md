<!-- Modified for k9+; see NOTICE. -->
# Capacity review validation — 2026-10-04

The #59 increment was checked with native Go tests and the actual application
running in a PTY against a disposable loopback Kubernetes API. These are fixture
checks; no live-cluster integration or human operator study is claimed.

The domain tests exercise effective init, restartable sidecar, overhead,
Pod-level and observed resize requirements; exhausted quota and missing quota
consumption; unavailable HPA inputs and incomplete configured metrics; optional
VPA recommendation bounds; API-group target matching; denied/partial node
visibility; bounded pages and replacement UIDs. Usage fixtures cover observed
zero, missing resources/containers, old/future samples, mismatched UIDs,
UID-less association and windows before Pod creation. A real HTTP cancellation
fixture proves a stalled node request does not discard independent readable
sources or survive cancellation. Collector tests pass with the race detector.

Native SimulationScreen checks inspect emitted cells at 120, 80, 60 and 40
columns, test the task-size notice, retain tab/search/scroll and failed-refresh
snapshots, reject destination/UID changes, and age fresh usage during native
Application.Draw without calling a locking Application getter from Draw.

`scripts/probe-capacity.py` drives the built application with real keyboard
input, captures emitted cells and records the API requests. It checks overview,
admission, scaling, nodes and evidence; unavailable usage becoming fresh after
explicit refresh; a failed refresh retaining the original time; Back restoring
the selected Deployment; and normal keyboard quit. The resize sequence is
120×34 → 80×24 → 60×24 → 40×16 → 40×12 notice → 60×24 with the Scaling tab
retained. No icons are enabled, and status meaning remains in plain text. The
journal verifies two fixed seven-source collections bounded at 101 requested
objects, no Secret API reads, and no resource writes. Authorization review POSTs
are not resource mutations.

Reproduce with Go 1.25.8 and Python with Pillow/pyte:

```sh
go test -p 2 ./internal/capacity ./internal/inspect ./internal/view \
  -run 'TestCollect|TestQuotaRemaining|TestRequirements|TestResourceBudgets|TestCapacity|TestPressure|TestAutoscaler|TestDeniedPods' -count=1
go test -race -p 2 ./internal/capacity -count=1
go build -buildvcs=false -o /tmp/k9plus-capacity .
python scripts/probe-capacity.py --repo "$PWD" --binary /tmp/k9plus-capacity \
  --output /tmp/k9plus-capacity-captures
```

The native capture directory contains PNGs, corresponding terminal text,
`api-requests.json` and `manifest.json` with the binary SHA-256 and capture
sizes/times. The development-session artifact is retained at
`/workspace/artifacts/k9plus-capacity-review-2026-10-04/pty`.

Whole-package local lint on the older integration base has no findings in the
capacity increment; remaining findings belong to pre-existing command
suggestions and desired/rollout rendering files. Current integration and hosted
checks are separate delivery gates.
