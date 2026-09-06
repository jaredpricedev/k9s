<!-- Modified for k9+; see NOTICE. -->

# Flux and reliability implementation plan

**Goal:** Improve the existing fork with verified fixes, measured table improvements and native Flux operations.

**Architecture:** Extend the existing DAO/model/view registrations. Pure `internal/flux` functions interpret unstructured Flux resources; native renderers and the combined view consume them. Use the existing connection and watch cache.

**Tech stack:** Go, client-go dynamic/informer APIs, k9s tview UI, YAML plugins.

**Spec:** `docs/superpowers/specs/2026-09-06-flux-review-design.md`

## Tasks

- [x] Compare fork history and fast-forward the isolated branch to upstream `84852e6`.
- [x] Establish test/build baseline with the repository's dependency versions.
- [x] Reproduce and fix `RowEvents.At` boundary panic and stale indexes in `internal/model1/row_event.go`; benchmark customization with 10,000 rows.
- [x] Add pure Flux status/source/dependency interpretation and native rendering, including stale generations and OCI chart references.
- [x] Add `dao.FluxDashboard.List` using discovered supported GVRs and the existing cache; retain accessible rows and display denied kinds.
- [x] Add tested native GET/PATCH Flux actions with explicit verbs, resource version preconditions, confirmation and read-only guards.
- [x] Register `:flux` and native per-kind views, with source/dependency navigation and normal YAML/describe support.
- [x] Repair Flux plugin resource scopes, context/kubeconfig forwarding, error handling and dangerous flags; document useful optional community integrations.
- [x] Run focused regressions, full suite, race checks on changed data handling, build and CLI smoke check.
- [x] Finish verification, review the diff independently, and resolve significant findings.

Tests use synthetic objects and fake API clients. Final review documentation records the actual commands and results. Delivery is through a branch and draft PR. No live cluster mutation is required.
