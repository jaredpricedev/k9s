<!-- Modified for k9+; see NOTICE. -->

# Daily Kubernetes workspace validation — 4 October 2026

The review branches implement the daily-app increments tracked by [issue #8](https://github.com/jaredpricedev/k9s/issues/8). Each pull request records its dependencies. The existing relationship PR stays separate. Review and merge are distinct steps; these results do not imply the changes are already shipped on master.

## Reproduce

Local checks pass: the complete CGO-disabled Go suite; the eight-package CGO-enabled race suite; lint (zero issues); four license collector tests; and the 379-module distribution bundle. Every focused stack branch also compiles and passes its selected standalone regressions. [Original output and slice revisions](evidence/validation-2026-10-04/README.md) are retained with the terminal evidence. The documented `make build` path succeeds with its default CGO0/netgo settings.

Use Go 1.25.8 from `go.mod`, golangci-lint v2.6, Python 3.12, a C compiler for race checks, and a UTF-8 terminal. The terminal captures additionally need the DejaVu Sans Mono fonts and the pinned packages below.

```sh
go test ./...
go test -race ./internal/client ./internal/model ./internal/dao ./internal/ui ./internal/view ./internal/inspect ./internal/logstream ./internal/hubble
golangci-lint run
python3 -m unittest discover -s scripts -p test_collect_licenses.py
make licenses
make build
./execs/k9plus help
./execs/k9plus info
python3 -m venv /tmp/k9plus-capture
/tmp/k9plus-capture/bin/pip install -r scripts/terminal-requirements.txt
/tmp/k9plus-capture/bin/python scripts/regression-journeys.py --binary ./execs/k9plus --output /tmp/k9plus-evidence/resources --slow-api --performance --investigations
go test -c -o /tmp/k9plus-view-tests ./internal/view
/tmp/k9plus-capture/bin/python scripts/hubble-latency.py --binary /tmp/k9plus-view-tests --output /tmp/k9plus-evidence/hubble
/tmp/k9plus-capture/bin/python scripts/skin-journeys.py --binary ./execs/k9plus --output /tmp/k9plus-evidence/skins
```

The Go suite isolates temporary directory fixtures and honors restrictive umasks. Use writable XDG application directories if running the CLI in a sandbox. `-buildvcs=false` is needed only where the execution environment prevents Go from reading Git metadata; normal checkout builds retain revision stamping.

## What the fixtures establish

The resource journey starts the actual application in a PTY against a disposable loopback API. It checks canonical Flux/native selection, malformed filter retention, valid zero-match recovery, safe synthetic views, explicit metrics unavailability, persistent access mode during prompts/errors/compact layout, and long Pod failure states at 80×24, 100×30 and 120×34. The skin journey covers stock, high-contrast and monochrome at true-color and 256-color terminal capabilities.

The selected-investigation journey checks resource/metrics diagnostics, explicit A/B observations, reversible API-noise normalization without another read, resource budgets/OOM/scheduling evidence, notes, private JSON export and equivalent offline Markdown import/export. It journals requests and asserts offline review performs no resource refetch or mutation. Secret bodies are covered by bounded domain and client tests rather than committed sensitive fixture data.

The delayed API journey holds a UID-conditioned restart PATCH for five seconds. Filtering, resizing and quit must complete before the response. API acceptance is separate from controller completion; cancellation does not undo an accepted write.

The Hubble journey runs native tview/tcell components with 10,000 retained synthetic events. It checks frozen body identity across a 10,000-event eviction burst, navigation/detail/back, and explicit resume. It does not connect to Relay. Its fixture is opt-in through the supplied script; ordinary tests never start a collector.

## Timing and limits

Performance reports distinguish ingestion, view assembly and terminal output. PTY timings measure input writes to asserted visible output parsed by pyte; they include Python parsing and Escape decoding. They do not measure a graphical terminal compositor. Reports retain samples, median/p95, workload, platform and binary hashes. The issue's p95≤100 ms goal is a documented local target, not a machine-independent CI gate.


The [native resource-filter report](filter-performance-2026-10-04.md) retains all 20 samples, strict committed-title/count/resource-row assertions, binary and source hashes, screenshots and the differing CGO build settings. The final sample misses the documented 100 ms tail target; it is not labeled as meeting it. The [Hubble report](hubble-performance-2026-10-04.md) records separate paired assembly/allocation and native keyboard measurements, with frozen-body checks during eviction.

| Native terminal fixture | Final observation | Evidence |
| --- | --- | --- |
| 10,000 Pods, 20 committed replacement queries | Median 80.366 ms; p95 107.274 ms; max 122.364 ms; p95≤100 ms: **false**; visible assertions 20/20 passed | [Original final JSON](evidence/filter-2026-10-04/final.json), [selected-row screenshot](evidence/filter-2026-10-04/final-pods-10000.png) |
| Restart PATCH held five seconds | Filter 13.435 ms; quit 15.604 ms; quit after 0.519 s while request remained pending | [Original final JSON](evidence/filter-2026-10-04/final.json), [pending-operation screenshot](evidence/filter-2026-10-04/final-pending-restart.png) |

CI retains ordinary tests, lint and license checks, adds CGO-enabled race coverage, and runs structural terminal assertions. Raw captures contain synthetic names and evidence. Do not replace reproducible assertions with screenshot pixel matching.

GitHub returned no workflow runs or commit statuses for the reviewed heads or the master baseline during this session. Local verification is recorded below; an empty status response does not establish a passing hosted CI run.

## Integration matrix

| Environment | Evidence | Scope |
| --- | --- | --- |
| Linux amd64, Go 1.25.8, Python 3.12 | Automated tests and actual PTY fixtures dated 4 October 2026 | Implemented paths and terminal output; fixture API behavior is explicit |
| Disposable kind Kubernetes 1.34.0 | Opt-in real API checks | UID/resource-version semantics and core workload observations; see integration test |
| macOS / Windows and other Kubernetes versions | Not exercised in this environment | Existing platform support is inherited; no new compatibility certification |
| Live Cilium/Hubble Relay, Gateway/HTTPRoute CRDs | No current live integration run | Relay readiness, pagination and relationship behavior use protocol/API fixtures |
| Human operator review | Pending | Automated journeys do not establish learnability, preference or task completion time |

To run the Kubernetes integration test, create your own disposable kind cluster named `k9plus-audit`, then pass its isolated kubeconfig. The test rejects other context names and non-loopback servers and creates/removes only its unique test namespace:

```sh
K9PLUS_AUDIT_KUBECONFIG=/absolute/path/to/kind-kubeconfig go test ./internal/view -run '^TestDisposableKubernetesOperationsAndPressure$' -count=1 -v
```

Before release, review the focused PRs together, rerun checks at the chosen revision, validate supported live integrations, update the feature matrix after merges, and perform an operator review. Merging or publishing is not performed by this validation pass.
