# Performance pass and comparison demo

**Goal:** Improve measured shared hot paths and demonstrate the cumulative difference from upstream using identical workloads.

**Approach:** Compare upstream `84852e6` (current master at measurement), pre-pass fork `5c5e1b1`, and the resulting fork on Go 1.25.8. Preserve UI semantics, features, skins, and custom columns. Profile before modifying production code. Measure sequentially with identical compiler/runtime settings. Keep raw results and capture scripts alongside a short video.

- [x] Establish clean detached baseline worktrees; add identical benchmark harnesses for table layout/refresh and existing customization/filter operations.
- [x] Profile shared UI work; optimize only confirmed overhead with regression coverage for ASCII, Unicode, markup, and row colors.
- [x] Inspect bulk row removal; preserve survivors, IDs, order, backing-slot clearing and input-set behavior while avoiding repeated scans.
- [x] Run repeated benchmarks for all three versions, recording timings, allocations and environment. Publish regressions as well as improvements.
- [x] Capture actual binaries using synthetic Kubernetes resources. Detect completed rendered responses, keep timestamped terminal evidence, and render equal-speed side-by-side MP4. No artificial delays or per-build speed changes.
- [x] Run full tests, lint, targeted races, build, and independent review. Update README/results and prepare the verified tree for the existing draft PR without merging.

The video measures local demo interactions; microbenchmarks exclude cluster/network latency and are reported separately. No speedup will be claimed for a feature absent upstream.
