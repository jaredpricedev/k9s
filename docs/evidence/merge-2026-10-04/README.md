<!-- Modified for k9+; see NOTICE. -->

# Relationship integration checks, 2026-10-04

These raw logs validate the integration prepared from daily-workspace source D
`b4e32fb0b231fb4414b2b9502bae01f21609891f`, normally merged with original PR #7 head
`c39fe730ce0bcd9c6c9b631d9fbb8f3cbed471eb`. The merge preserves both histories.

Additional integration fixes retain source and destination UID guards, reject a
matching relationship row after a context change, preserve retained inspection
search/highlight/scroll state through skin changes and Back, and restore the
per-jump source namespace only while the jump still owns its destination.
A portable `atomic.Uint64` destination revision also rejects context/namespace
changes away and back. Navigation cancellation and expired queued replies have
regressions. The existing action overlay, operations and filter lifecycle remain.

Checks used Go 1.25.8 linux/amd64 and golangci-lint 2.6.0, with
`GOCACHE=/workspace/.cache/go-build`, `GOPATH=/workspace/go`,
`XDG_CACHE_HOME=/workspace/.cache` and workspace lint caches.

| Check | Raw output | Result |
| --- | --- | --- |
| Complete ordinary suite, CGO disabled | [ordinary.txt](ordinary.txt) | Pass |
| Eight-package race suite, CGO enabled | [race.txt](race.txt) | Pass |
| Full lint, CGO enabled | [lint.txt](lint.txt) | Zero issues |

```sh
GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 -buildvcs=false ./... -count=1
GOMAXPROCS=2 CGO_ENABLED=1 go test -race -p 2 -buildvcs=false ./internal/client ./internal/model ./internal/dao ./internal/ui ./internal/view ./internal/inspect ./internal/logstream ./internal/hubble -count=1
GOMAXPROCS=2 CGO_ENABLED=1 GOFLAGS=-buildvcs=false golangci-lint run ./...
```

The checks ran on the final integration Go source before its local merge commit
was recorded. Its 12 Go paths changed from D have combined SHA256
`65a5d7f21fa1021e0873ec348f3fc271e1c2bf69b33cc649179a544831968b34`
(sorted relative path, NUL, file bytes, NUL for each path). Documentation and
inventory were finalized afterward. VCS stamping was disabled for sandbox reads.

This record makes no hosted-CI, CLI, terminal-journey or live-cluster claim for the
new merge head. The earlier source C/D report and raw evidence remain unchanged;
subsequent merge-head checks must retain their own source and binary identity.

## Follow-up source checks

These separate logs cover the source fixes prepared on top of integration and
documentation head `a67b0fe89672ce4bfad461f8711fddf320b8820c`, before the
consolidated follow-up commit. The original merge history from D and PR #7 is
retained. The initial raw logs above and the dated C/D evidence remain unchanged.

Reactive configuration reloads now invalidate destination ownership after actual
namespace or context changes. Valid nested Back navigation renews only the exact,
still-owned ancestor tickets captured before the child jump. Manual, reactive
and context changes away and back continue to invalidate those tickets.
Regressions cover nested returns through three transitions, retained UID,
snapshot, query, highlight and scroll state, and intervening user changes.

A matching relationship row directs live YAML, Describe and kubectl Edit to an
explicitly reopened native list, while Inspector retains UID-checked evidence.
Native action regressions cover stale context/UID rejection and normal unrelated
rows. Compact metric wording and header column sizing have draw-loop regressions.
Atomic file publication also fixes the cross-process test helper's readiness and
result exchange; it makes no change to production lease logic.

| Check | Raw output | Result |
| --- | --- | --- |
| Complete ordinary suite, CGO disabled | [ordinary-followup.txt](ordinary-followup.txt) | Pass |
| Eight-package race suite, CGO enabled | [race-followup.txt](race-followup.txt) | Pass |
| Full lint, CGO enabled | [lint-followup.txt](lint-followup.txt) | Zero issues |
| Cross-process publication fixtures, 50 repetitions | [publication-50.txt](publication-50.txt) | Pass |

The first three checks used the same Go 1.25.8, lint 2.6.0, caches, environment and
commands recorded above. Their exact source has 24 Go paths changed from D and combined
SHA256 `df31740778de24f2e1ea09497d6480b422d45be226e043edb83e3c627b7b3e77`,
using the same sorted path/NUL/bytes/NUL algorithm. All three checks passed on
this source after the final test-only lint cleanup. Documentation was finalized
afterward.

The publication fixtures passed 50 repetitions before the other follow-up edits;
their helper source is identical in the final source. The exact command was:

```sh
GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 -buildvcs=false ./internal/view -run '^(TestPrepareLogSessionWaitsForCrossProcessRootLease|TestConcurrentProcessesPublishOnlyOneLeasedSessionAtCap)$' -count=50
```

This follow-up record reports local source checks. A clean binary, terminal
journeys and subsequent hosted CI require their own source and binary identity.
