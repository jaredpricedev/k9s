# Hubble rendering evidence — 2026-10-04

Frozen conversations keep their captured event IDs and table cells while new
flows update collector status and eviction counters. Live views take a new
snapshot only when the store revision changes. Skin, filter and mode changes
invalidate the relevant rows; explicit resume displays the current live data.

## Workload and provenance

The fixture retains 10,000 synthetic flows and adds one flow before each measured
render. It contacts no Relay or Kubernetes API. Frozen and live measurements are
separate. The test binary uses Go 1.25.8, ordinary `gc`/`exe` build settings,
CGO enabled, linux/amd64 and GOAMD64=v1, without race instrumentation or custom
build flags. The KVM host reports an AMD EPYC 9V74 and five online virtual CPUs;
both runs use GOMAXPROCS=4 without CPU affinity. Other Go builds and benchmarks
were held during the paired measurements, but this remains a shared host.

The final binary was built at 00:54 UTC from the future #8 worktree based on
`4be90d992e080c8e7775b028afae9d78423e50bb`. The worktree contains uncommitted
feature changes; later resolver string constant cleanup changes no runtime
behavior. The saved binary hashes identify the measured artifacts:

- Before profile refinements:
  `55ead4573d6036b0e110c270209c5a245bb5a82cf1e8ae3abfa116a56fe0730d`.
- Final:
  `fbf00a9e4774e6b7d40bad4d2bc3a246736e20a0f291d541d852f05e018d26c6`.

The paired earlier binary already contains frozen row retention and revision
invalidation. It precedes cached readable row colors and reuse of projection
capacity. It is **not** the original audit baseline. The original issue's
37.1–47.5 ms/~34.4 MB frozen result came from another environment and is not a
controlled timing comparison with these runs.

Raw results, build metadata, source file hashes and methodology are retained in
[the evidence directory](evidence/hubble-2026-10-04/provenance.json).

## View assembly and allocation

Each mode ran three 200 ms rounds. Values below span all three rounds; allocated
bytes describe allocation totals per operation, not resident memory. No terminal
painting occurs in this benchmark.

| Mode | Before profile refinements | Final | Final allocations |
| --- | --- | --- | --- |
| Frozen, status update with one new flow | 16.03–16.34 µs/op; 8.75 KB/op | 17.92–26.21 µs/op; 8.74–8.76 KB/op | 84/op |
| Live, changed 10,000-flow dataset | 59.88–68.67 ms/op; 34.20–34.24 MB/op | 43.99–59.43 ms/op; 18.06–18.08 MB/op | 400,151–400,154/op |

The frozen result retains the table rather than rebuilding 10,000 rows. Its
remaining allocations include status formatting and ingestion; the cheap store
counter/revision read itself allocates zero bytes. Regression tests check the
exact retained dataset backing storage and cell pointers while live events are
evicted, and verify filter changes and explicit resume.

Live rounds execute only 4–6 measured operations each, so their timing range is
noisy. Separate 400 ms CPU/allocation profiles informed the refinement: readable
colors no longer require contrast calculation for every cell, and projection
slices retain capacity. The remaining live work includes table cell creation,
literal escaping, snapshot copies and garbage collection. This increment keeps
the native table viewport and scrolling behavior. It does not add a new virtual
table implementation.

Raw [before](evidence/hubble-2026-10-04/before-benchmarks.txt) and
[final](evidence/hubble-2026-10-04/final-benchmarks.txt) benchmark output is included.

## Native terminal keyboard response

`scripts/hubble-latency.py` drives a real 120×40 PTY using the native tview/tcell
fixture. It measures a key write through the completed frame/footer received and
parsed by pyte, with 20 observations per action. This includes scheduler and
Python parser overhead. Escape measurements also include the terminal key
decoder's delay. It does not measure a graphical terminal compositor, physical
display latency or human task performance.

| Action | Before median / p95 | Final median / p95 |
| --- | --- | --- |
| Navigate to the next frozen flow | 4.181 / 5.840 ms | 3.988 / 8.345 ms |
| Enter flow details | 6.084 / 8.135 ms | 5.506 / 8.020 ms |
| Escape back to conversation | 66.752 / 72.009 ms | 64.907 / 73.289 ms |

The reported p95 is the 19th sorted observation of 20. This small sample does not
establish a tail confidence bound or a statistically significant keyboard speed
improvement. Fixture frames are input driven; the production collector status
interval is 250 ms (4 Hz), and the collector is not running in this fixture.

A single 10,000-event burst evicted the entire live buffer. The final frozen
conversation remained on event ID 1 and its rendered body hash stayed identical:
`a514ce0f4735e7c87eff6850666765c167dd6433ba5f81991ce8d95e8c8e9fb8`.
The eviction counter updated to 10,000 in 4.899 ms. Explicit resume displayed
current live event ID 10001 in 48.789 ms. These are single observations, not
latency distributions. The paired earlier build also preserved the frozen body;
its burst and resume observations were 3.652 ms and 76.945 ms.

Raw [before](evidence/hubble-2026-10-04/before-latency.json) and
[final](evidence/hubble-2026-10-04/final-latency.json) samples are included. Full
profiles, emitted terminal bytes and screen captures are retained in the task's
`/workspace/k9plus-evidence/hubble` artifact directory.

## Reproduce

Build the view test binary once and use it for both measurement types. The PTY
runner requires Python 3.11+, pyte and Pillow. It uses only the opt-in fixture;
ordinary test runs skip the interactive terminal test.

```sh
go test -c ./internal/view -o /tmp/k9plus-view-tests
GOMAXPROCS=4 /tmp/k9plus-view-tests -test.run='^$' \
  -test.bench='^BenchmarkHubbleConversationRender$' \
  -test.benchmem -test.benchtime=200ms -test.count=3
python scripts/hubble-latency.py --binary /tmp/k9plus-view-tests \
  --output /tmp/hubble-evidence --samples 20 --gomaxprocs 4
```

The bounded workload resolver has separate disposable HTTP API tests: pages of
at most 250 pods, a final request for one extra match at the cap, and no requests
after 1001 matches establish refusal. Tests cover 0, 3, 1000, 1001 and 10,000
matches, repeated continuation tokens, later-page permission failure, selected
UID replacement, cancellation and clients pinned across a context switch.
