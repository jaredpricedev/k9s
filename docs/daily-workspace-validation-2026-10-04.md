<!-- Modified for k9+; see NOTICE. -->

# Daily toolkit workspace validation — 4 October 2026

This record covers the first toolkit Horizon 1 delivery (TK01–TK05), building on
default-branch revision `1978cb74caab53f74403e9e776d18bb2975e01e4`. It is separate
from the earlier [daily-app validation](validation-2026-10-04.md).

The native terminal journeys ran the actual application against disposable
loopback API fixtures. The final daily-workspace capture uses Go 1.25.8 and
source `a84ce5ca148da9c0904d8d8f8dae70ca9e56544d`; its binary SHA-256 is
`38e007bc0869b219d5b0607ad11a157bc2656452938cf23802e4bd2fcb9fb048`.
The investigation/resource/skin captures use Go 1.27.1 and functional source
`54c21d80c90b57cbbb8da826c9b9f9fb3f789aa6`; their binary SHA-256 is
`13fc62a7137598f8e3d34590d49e2abbf7a8d57970d1d061f96270610ae86c6c`.
Both builds disable CGO, use netgo and `-buildvcs=false`. The manifests retain
which binary ran each journey. Later formatting and documentation commits do
not replace these recorded source identities.
These checks establish fixture behavior and emitted terminal cells. They do not
establish live-provider compatibility or human usability targets.

| Check | Observed result |
| --- | --- |
| Complete CGO-disabled Go suite, Go 1.25.8 and Go 1.27.1 | Passed |
| Pinned golangci-lint v2.6.2 with Go 1.25.8 | Passed; zero issues |
| CGO-enabled race checks in client/model/dao/ui/view/inspect/logstream/hubble/workspace | Passed |
| Strict daily-workspace native journey | Passed; 21 captures, including 80×24 resize and app restart |
| Investigation navigation, search and retained failure | Passed at 80×24 and 120×34 |
| Existing resource and selected-investigation journeys | Passed at 80×24, 100×30 and 120×34, plus evidence round-trip |
| Stock, high-contrast and monochrome skins | Passed at 80×24 in true-color and 256-color modes |
| Four license collector tests and eleven-platform dependency bundle | Passed; 379 modules |

The workspace journey checks creation/cancellation, exact namespace and label
scope, kind-aware findings, denied coverage, UID-bearing pins, local search,
investigation/evidence entry, replaced-Pod log refusal, native live logs,
successful partial refresh, total-failure retention, connection retry and private
metadata persistence. The request journal records 84 workspace inventory LISTs:
every one uses `apps` or `ops`, exact selector `app=daily`, and limit 200. Native
log discovery uses a named Pod field selector and limit 128; connection probes
use one namespace and limit 1. Inherited session startup/header requests also
occur and are recorded separately. No Secret request or Kubernetes mutation
occurred in this journey.

The existing 10,000-Pod filtering fixture passed its structural assertions while
missing its 100 ms p95 target: median 127.221 ms, p95 176.232 ms. This run shared
the host with other checks, so it does not establish a controlled before/after
performance comparison. The delayed restart fixture remained responsive while
its five-second API request was pending. Neither measurement is a human task
completion time.

Saved metadata uses atomic mode-0600 writes. Collection is explicitly refreshed,
bounded and non-atomic across APIs. Connection retry rebuilds diagnostic clients;
ordinary browsing reconnects through an explicit context selection. Historical
activity, automatic remediation and fleet/provider expansion remain later work.
Issues #8, #22 and #23 retain their outstanding operator evaluation requirements.

See [the evidence index](evidence/daily-workspace-toolkit-2026-10-04/README.md)
and [usage guide](daily-workspace.md). Reproduce with the repository's versions
from `go.mod` and `.github/workflows/lint.yml`, plus
`scripts/terminal-requirements.txt`:

Local lint additionally set `GOFLAGS='-p=4 -buildvcs=false'` to avoid restricted
Git metadata access. Its first cold dependency load exceeded the five-minute
timeout; the completed cached run uses the repository's normal lint rules.

```sh
go test ./...
go test -race -p 4 ./internal/client ./internal/model ./internal/dao ./internal/ui ./internal/view ./internal/inspect ./internal/logstream ./internal/hubble ./internal/workspace
golangci-lint run
go build -tags netgo -o /tmp/k9plus .
python3 scripts/daily-workspace-journeys.py --binary /tmp/k9plus --output /tmp/daily-workspace-evidence
python3 scripts/regression-journeys.py --binary /tmp/k9plus --output /tmp/resource-evidence --slow-api --performance --investigations
python3 scripts/skin-journeys.py --binary /tmp/k9plus --output /tmp/skin-evidence
```
