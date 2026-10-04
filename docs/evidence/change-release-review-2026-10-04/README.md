# Change and release review — actual terminal evidence

All 18 workflow checks passed at **80×24** and **120×34** on 2026-10-04. The app ran in read-only mode against a disposable API on `127.0.0.1`; every workload, context and credential marker is synthetic. PNGs contain actual PTY terminal cells rasterized with pyte/Pillow. Adjacent TXT files preserve the emitted text, and `.pages.txt` files include the full visited retained detail.

[manifest.json](manifest.json) records the checks, files and hashes. Final capture completed at `2026-10-04T12:28:23Z` in 53.488 seconds.

- Source commit used for the build: `8b377c660b46c5c921608d70363fbf926a7ee34f`.
- [Build metadata](build-info.txt): Go `1.25.8`, Linux/amd64, `netgo`, `CGO_ENABLED=0`.
- [App version output](version-info.txt): `v0.1.0-dev`, embedded commit `dev`, embedded date `n/a`. The source commit above is build provenance supplied separately from those development labels.
- Binary SHA-256: `26c60694130fe6381e29c571e4497dcb3c0b3ceb815e4991b773482f486d749c`.
- [Journey script](../../../scripts/change-release-journeys.py) SHA-256: `73972cfed2b3649b579189140983f5a773d01d9010dc107b9a8fa0a05e4fb021`.

| Guide screen | 80×24 | 120×34 |
| --- | --- | --- |
| Manifest resource table | [PNG](80x24-desired-resource-table.png) · [TXT](80x24-desired-resource-table.txt) | [PNG](120x34-desired-resource-table.png) · [TXT](120x34-desired-resource-table.txt) |
| Authored image and memory changes | [PNG](80x24-desired-authored-detail.png) · [TXT](80x24-desired-authored-detail.txt) | [PNG](120x34-desired-authored-detail.png) · [TXT](120x34-desired-authored-detail.txt) |
| Rollout progress and replica counts | [PNG](80x24-rollout-progress-overview.png) · [TXT](80x24-rollout-progress-overview.txt) | [PNG](120x34-rollout-progress-overview.png) · [TXT](120x34-rollout-progress-overview.txt) |
| Explicit historical-template preview | [PNG](80x24-rollout-historical-preview.png) · [TXT](80x24-rollout-historical-preview.txt) | [PNG](120x34-rollout-historical-preview.png) · [TXT](120x34-rollout-historical-preview.txt) |

The desired-state journey opens a file whose name contains a space, shows the authored image and resource changes, and checks that omitted server defaults do not become deletion proposals. Rewriting that file leaves the retained source hash and authored image fixed during `r`. Denied and replacement-UID reads retain the original evidence.

The rollout journey excludes descendants with foreign owner UIDs or `controller=false`, retains redacted workload values in the Evidence tab, and distinguishes an unobserved generation from an older blocked condition. It selects a historical ReplicaSet with `j/k` and Enter, then proves the preview keeps its original UID/template after the server fixture changes. Retained tabs and template selection issue no additional API requests. Denied/recreated Deployment refreshes retain the captured time and stop before descendant reads.

Each [80-column](80x24-request-journal.json) and [120-column](120x34-request-journal.json) journal contains **49 requests**, including **15 named Deployment/descendant resource reads**. The audit permits GETs and SelfSubjectAccessReview permission-check POSTs, and rejects workload mutations, Secret reads and reads of the out-of-scope manifest namespace. The synthetic credential marker is absent from every saved TXT file; raw authored source files remain temporary.

This evidence validates the captured development binary with synthetic fixtures. It does not establish live-cluster availability, admission/defaulting behavior, rollback execution or a human usability study. Deployment status convergence and child collection coverage remain separate observations. Go test and race results are tracked separately.

To reproduce with a new build and output directory, install [terminal requirements](../../../scripts/terminal-requirements.txt) and run from the repository root:

```sh
python scripts/change-release-journeys.py \
  --binary /path/to/k9plus \
  --output /tmp/change-release-evidence
```
