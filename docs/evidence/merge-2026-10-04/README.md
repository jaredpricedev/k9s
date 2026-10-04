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
