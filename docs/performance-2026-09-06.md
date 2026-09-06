# Performance comparison — 2026-09-06

This pass fixes quadratic row removal, avoids a duplicate snapshot allocation, computes row color once per visible row, and restores an ASCII fast path while retaining correct Unicode and markup widths. It also fixes an upstream test fixture that used sleep instead of synchronization.

## Builds and method

- **Upstream:** `84852e6e47ae830b30c921ffa5838443387e096e`, verified as upstream master at the start of the pass.
- **Previous fork:** `5c5e1b1dcf708373deab91add79800ff2572692d`, including all prior Flux, certificate and UI changes.
- **Updated fork:** that same fork plus this performance pass.

All builds use Go 1.25.8, identical go.mod/go.sum files, linux/amd64 and the same host (Intel Xeon Platinum 8573C). Benchmark fixtures are byte-identical across the three worktrees. Five rounds run sequentially, rotating build order, with `GOMAXPROCS=4` and a 200 ms Go benchmark target per case. Build/fixture setup is excluded from operation timing.

This is a shared host: timing samples vary, sometimes substantially. The tables show medians, not statistical significance. Raw samples, ranges, fixture hashes and environment details are in [benchmark results](../assets/performance/benchmarks/results.json); individual Go logs are in the same directory. Small timing differences should not be treated as guaranteed latency improvements. Allocation counts and the bulk-removal improvement are more stable evidence.

## All measured CPU workloads

All timings below are milliseconds per operation.

| Workload | Upstream | Previous fork | Updated fork |
| --- | ---: | ---: | ---: |
| Column widths, 1,000 rows | 0.008 | 11.114 | 0.065 |
| Column widths, 10,000 rows | 0.085 | 112.644 | 0.688 |
| Table rebuild + viewport draw, 1,000 rows | 5.670 | 35.307 | 6.350 |
| Table rebuild + viewport draw, 10,000 rows | 33.171 | 330.341 | 25.798 |
| Customize columns, 10,000 rows | 4.951 | 2.561 | 2.121 |
| Regex filter, 10,000 rows | 11.122 | 9.726 | 9.289 |
| Deep snapshot, 10,000 rows | 3.130 | 3.188 | 2.520 |
| Refresh deletion phase, no removed rows | 0.452 | 0.530 | 0.417 |
| Remove 100 of 10,000 rows | 23.050 | 24.316 | 1.025 |
| Remove 5,000 of 10,000 rows | 834.884 | 784.492 | 1.607 |

The 5,000-row removal phase is about 520× faster in this fixture because it no longer shifts and reindexes the table separately for every removed row. That ratio applies to the deletion phase only, not the whole application or Kubernetes operations. The no-removal case is included to check ordinary refresh overhead.

The earlier UI polish introduced a measurable regression: grapheme/markup parsing ran for ordinary ASCII cells. The profile attributed about 70% of sampled CPU time to tview string decomposition. The fast path removes that unnecessary parsing; non-ASCII text, control characters and potential tags still use the original parser. The updated 10,000-row table case is about 12.8× faster than the previous fork in these samples.

This is not a uniform speedup over upstream. Width calculation still costs more than upstream’s byte-count approach because the fork checks for text that needs Unicode/markup handling. The 1,000-row table median was about 12% slower than upstream, with overlapping sample ranges. The 10,000-row table median was about 22% lower, also with variable samples. These results are included rather than selecting only favorable cases.

## Allocation differences

| Workload, 10,000 rows | Upstream bytes/op | Previous fork bytes/op | Updated fork bytes/op |
| --- | ---: | ---: | ---: |
| Customize columns, 10,000 rows | 5,191,309 | 1,797,900 | 1,797,844 |
| Regex filter, 10,000 rows | 1,875,666 | 587,620 | 586,588 |
| Deep snapshot, 10,000 rows | 3,030,746 | 3,030,744 | 2,309,843 |
| Table rebuild + viewport draw, 10,000 rows | 14,387,117 | 19,728,688 | 14,395,003 |

The earlier column customization and regex changes retain their allocation savings (about 65% and 69% fewer bytes than upstream here). Snapshot cloning now avoids one approximately 721 KB row slice per 10,000-row clone, while preserving independent fields and deltas.

## What each benchmark includes

- **Column widths:** the shared width pass across eight fields per row.
- **Table rebuild + draw:** actual table-cell construction, sort/index handling, padding and drawing a 160×40 simulated viewport. Prepared data is reused; subsequent iterations are already sorted. It excludes filtering, cloning, customization, API traffic and terminal flushing.
- **Customization:** creating a four-column projection from six-column rows.
- **Regex:** the same case-insensitive expression over joined fields; 1,000 of 10,000 fixture rows match.
- **Snapshot:** deep-copying 10,000 rows, including deltas on every fifth row.
- **Deletion:** removing absent row IDs, preserving survivor order and indexes. Each iteration receives a fresh identical table and keep-set outside its timed region.

## Reproduce

Create detached worktrees at the upstream and previous-fork revisions above. Build each CLI with the same Go binary and flags:

```sh
GOTOOLCHAIN=local GOMAXPROCS=4 go build -buildvcs=false -p 2 -o /tmp/k9s-BUILD .
```

Run from the updated fork:

```sh
python scripts/benchmark-performance.py \
  --upstream /path/to/upstream-worktree \
  --before /path/to/previous-fork-worktree \
  --fork . --go /path/to/go1.25.8/bin/go
```

The script copies only the four portable benchmark test files into the baseline worktrees, compiles all harnesses before measurement, rotates execution order, and writes raw logs plus JSON. It refuses to replace differing tracked baseline fixture files. Production sources and dependencies stay unchanged.

## Actual-binary video

[Watch the short comparison](../assets/performance/comparison.mp4). All three panels replay actual terminal output at the same 1× speed; there is no per-build speed adjustment. A median-nearest sample is selected independently for each build and operation. Completed panels hold their final frame. The 9.36-second video ends with a four-second static card of the separate CPU measurements, explicitly labeled with their scope.

Each build loads the same 10,000 synthetic ConfigMaps through a disposable localhost API. Enter applies a prepared `/7` filter (3,439 matches); another prepared Enter clears it back to 10,000 rows. Three rounds rotate build order, with two warmups and five retained samples per operation per round: 15 samples per build/operation. All runs use isolated K9s/discovery caches, the same two-second refresh interval, a 100×28 PTY, read-only mode and `GOMAXPROCS=2`.

Timing starts immediately before Enter and ends at receipt of a matching terminal frame, checked against the exact row count and visible name sequence. It excludes typing/preparation and does not isolate all filter computation. It includes application scheduling and terminal output; it does not measure physical display latency. Startup is recorded separately and is not mixed into the warm interaction medians.

| Enter-to-matching-frame median | Upstream | Previous fork | Updated fork |
| --- | ---: | ---: | ---: |
| Apply prepared filter | 91.2 ms | 114.9 ms | 84.4 ms |
| Clear filter / restore rows | 84.2 ms | 188.3 ms | 83.2 ms |

The real UI is close to upstream in this fixture. The small upstream-to-fork differences overlap the sample ranges and are not evidence of a guaranteed visible speedup. Restoring the full list shows a clearer improvement over the previous fork. The much larger bulk-removal speedup belongs to the separate CPU benchmark, not this filter video.

[Capture results](../assets/performance/results.json) include every sample, ranges, compiler/build metadata, binary hashes and the selected video samples. Timestamped terminal traces are retained as losslessly compressed `.cast.gz` files alongside the video. The initial failed capture was a harness setup problem: a 3,600-second refresh interval could leave a large informer load waiting after the first empty read. The successful comparison uses the same normal interval and isolated discovery cache for every build.

Reproduce the video after building the three binaries:

```sh
python scripts/perf-demo.py \
  --upstream /tmp/k9s-upstream --before /tmp/k9s-before --fork /tmp/k9s-fork \
  --go-tool /path/to/go1.25.8/bin/go \
  --upstream-revision 84852e6 --before-revision 5c5e1b1 \
  --fork-revision YOUR_TESTED_REVISION
```

The capture script needs Python packages `Pillow` and `pyte`, plus ffmpeg. `--render-only assets/performance/results.json` replays the saved evidence without rerunning K9s. No production cluster or credentials are used.

## Verification

The updated tree passed the full Go test suite, configured golangci-lint (zero issues), race checks for `internal/model1` and `internal/ui`, and the CLI build. Independent review covered the width fast path and lazy row coloring. Regression coverage preserves Unicode/markup semantics, deep-copy independence, stable deletion order, duplicate-ID behavior, indexes and clearing removed backing references.

The broader UI race run exposed an unchanged upstream Flash test that read text concurrently after a sleep. The test now waits for the completed-write notification and joins its watcher; production Flash behavior was not changed. The full UI race run then passed.
