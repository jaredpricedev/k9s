<!-- Modified for k9+; see NOTICE. -->

# k9+ modification notice

k9+ is an independent, modified distribution of [k9s](https://github.com/derailed/k9s).
Original copyright and license notices are retained. These changes are offered
under Apache-2.0; see LICENSE, COPYING, NOTICE and docs/licensing.md.

The upstream comparison point is `84852e6e47ae830b30c921ffa5838443387e096e`.
This fork adds native Flux and cert-manager workflows, inline asynchronous Flux
mutations, reliability and read-only fixes, UI polish, performance improvements,
reproducible demos, and the k9+ application identity and distribution setup.
Source comments identify modified files; Git history records individual changes.
Historical benchmark results and terminal recordings retain the exact identity
and bytes observed in their original runs.

The internal Go module path, compatible configuration keys, schema names and
Kubernetes annotation conventions remain from k9s to preserve interoperability.
They identify technical interfaces and provenance, not upstream endorsement.

The selected follow-up logo replaces the outlined ASCII wordmark with a five-row,
32-column Unicode block `[k9+]`, with skin-aware accents in the header and splash.
CLI output shares the glyphs; fresh screenshots show the running application.

## Changed files relative to upstream

This inventory includes additions, modifications and removals on the review
branch. Generated THIRD_PARTY_LICENSES is assembled separately for distributions.

- `.dockerignore`
- `.github/FUNDING.yml`
- `.github/ISSUE_TEMPLATE/bug_report.md`
- `.github/ISSUE_TEMPLATE/feature_request.md`
- `.github/workflows/lint.yml`
- `.github/workflows/test.yml`
- `.gitignore`
- `.goreleaser.yml`
- `Dockerfile`
- `MODIFICATIONS.md`
- `Makefile`
- `NOTICE`
- `README.md`
- `assets/block-logo/README.md`
- `assets/block-logo/capture.json`
- `assets/block-logo/certificate-relationships.png`
- `assets/block-logo/certificate-relationships.txt`
- `assets/block-logo/certificate-status.png`
- `assets/block-logo/certificate-status.txt`
- `assets/block-logo/certificates-overview.png`
- `assets/block-logo/certificates-overview.txt`
- `assets/block-logo/flux-kustomizations.png`
- `assets/block-logo/flux-kustomizations.txt`
- `assets/block-logo/flux-overview.png`
- `assets/block-logo/flux-overview.txt`
- `assets/block-logo/flux-reconcile.png`
- `assets/block-logo/flux-reconcile.txt`
- `assets/block-logo/flux-relationships.png`
- `assets/block-logo/flux-relationships.txt`
- `assets/flux-inline/01-ready-10000.png`
- `assets/flux-inline/01-ready-10000.txt`
- `assets/flux-inline/02-confirm-native.png`
- `assets/flux-inline/02-confirm-native.txt`
- `assets/flux-inline/03-accepted-patch-held.png`
- `assets/flux-inline/03-accepted-patch-held.txt`
- `assets/flux-inline/04-filtered-moving-patch-held.png`
- `assets/flux-inline/04-filtered-moving-patch-held.txt`
- `assets/flux-inline/05-controller-waiting.png`
- `assets/flux-inline/05-controller-waiting.txt`
- `assets/flux-inline/06-ready-acknowledged.png`
- `assets/flux-inline/06-ready-acknowledged.txt`
- `assets/flux-inline/07-failed-acknowledged.png`
- `assets/flux-inline/07-failed-acknowledged.txt`
- `assets/flux-inline/08-ready-and-failed-10000.png`
- `assets/flux-inline/08-ready-and-failed-10000.txt`
- `assets/flux-inline/README.md`
- `assets/flux-inline/api-requests.json`
- `assets/flux-inline/inline-reconcile.mp4`
- `assets/flux-inline/k9s.log`
- `assets/flux-inline/reconciling.png`
- `assets/flux-inline/results.json`
- `assets/flux-inline/session.cast.gz`
- `assets/k9plus/README.md`
- `assets/k9plus/capture.json`
- `assets/k9plus/certificate-relationships.png`
- `assets/k9plus/certificate-status.png`
- `assets/k9plus/certificates-overview.png`
- `assets/k9plus/flux-kustomizations.png`
- `assets/k9plus/flux-overview.png`
- `assets/k9plus/flux-reconcile.png`
- `assets/k9plus/flux-relationships.png`
- `assets/k9plus/inline-smoke.json`
- `assets/k9plus/reconciling.png`
- `assets/performance/api-requests.json`
- `assets/performance/before-1.cast.gz`
- `assets/performance/before-1.screen.txt`
- `assets/performance/before-2.cast.gz`
- `assets/performance/before-2.screen.txt`
- `assets/performance/before-3.cast.gz`
- `assets/performance/before-3.screen.txt`
- `assets/performance/benchmark-summary.png`
- `assets/performance/benchmarks/profile-before.txt`
- `assets/performance/benchmarks/results.json`
- `assets/performance/benchmarks/round-1-before-model1.txt`
- `assets/performance/benchmarks/round-1-before-ui.txt`
- `assets/performance/benchmarks/round-1-fork-model1.txt`
- `assets/performance/benchmarks/round-1-fork-ui.txt`
- `assets/performance/benchmarks/round-1-upstream-model1.txt`
- `assets/performance/benchmarks/round-1-upstream-ui.txt`
- `assets/performance/benchmarks/round-2-before-model1.txt`
- `assets/performance/benchmarks/round-2-before-ui.txt`
- `assets/performance/benchmarks/round-2-fork-model1.txt`
- `assets/performance/benchmarks/round-2-fork-ui.txt`
- `assets/performance/benchmarks/round-2-upstream-model1.txt`
- `assets/performance/benchmarks/round-2-upstream-ui.txt`
- `assets/performance/benchmarks/round-3-before-model1.txt`
- `assets/performance/benchmarks/round-3-before-ui.txt`
- `assets/performance/benchmarks/round-3-fork-model1.txt`
- `assets/performance/benchmarks/round-3-fork-ui.txt`
- `assets/performance/benchmarks/round-3-upstream-model1.txt`
- `assets/performance/benchmarks/round-3-upstream-ui.txt`
- `assets/performance/benchmarks/round-4-before-model1.txt`
- `assets/performance/benchmarks/round-4-before-ui.txt`
- `assets/performance/benchmarks/round-4-fork-model1.txt`
- `assets/performance/benchmarks/round-4-fork-ui.txt`
- `assets/performance/benchmarks/round-4-upstream-model1.txt`
- `assets/performance/benchmarks/round-4-upstream-ui.txt`
- `assets/performance/benchmarks/round-5-before-model1.txt`
- `assets/performance/benchmarks/round-5-before-ui.txt`
- `assets/performance/benchmarks/round-5-fork-model1.txt`
- `assets/performance/benchmarks/round-5-fork-ui.txt`
- `assets/performance/benchmarks/round-5-upstream-model1.txt`
- `assets/performance/benchmarks/round-5-upstream-ui.txt`
- `assets/performance/comparison.mp4`
- `assets/performance/comparison.png`
- `assets/performance/fork-1.cast.gz`
- `assets/performance/fork-1.screen.txt`
- `assets/performance/fork-2.cast.gz`
- `assets/performance/fork-2.screen.txt`
- `assets/performance/fork-3.cast.gz`
- `assets/performance/fork-3.screen.txt`
- `assets/performance/results.json`
- `assets/performance/upstream-1.cast.gz`
- `assets/performance/upstream-1.screen.txt`
- `assets/performance/upstream-2.cast.gz`
- `assets/performance/upstream-2.screen.txt`
- `assets/performance/upstream-3.cast.gz`
- `assets/performance/upstream-3.screen.txt`
- `assets/screenshots/certificate-relationships.png`
- `assets/screenshots/certificate-status.png`
- `assets/screenshots/certificates-overview.png`
- `assets/screenshots/flux-kustomizations.png`
- `assets/screenshots/flux-overview.png`
- `assets/screenshots/flux-reconcile.png`
- `assets/screenshots/flux-relationships.png`
- `cmd/branding_test.go`
- `cmd/info.go`
- `cmd/root.go`
- `cmd/version.go`
- `docs/certificates.md`
- `docs/flux-status-benchmark-2026-09-06.txt`
- `docs/flux.md`
- `docs/licensing.md`
- `docs/migration.md`
- `docs/performance-2026-09-06.md`
- `docs/review-2026-09-06.md`
- `docs/superpowers/plans/2026-09-06-certificate-polish.md`
- `docs/superpowers/plans/2026-09-06-flux-review.md`
- `docs/superpowers/plans/2026-09-06-performance-pass.md`
- `docs/superpowers/specs/2026-09-06-flux-review-design.md`
- `go.mod`
- `internal/certmanager/resource.go`
- `internal/certmanager/resource_test.go`
- `internal/client/gvrs.go`
- `internal/config/alias.go`
- `internal/config/alias_test.go`
- `internal/config/cert_manager_plugins_test.go`
- `internal/config/cnpg_plugins_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/data/helpers.go`
- `internal/config/files.go`
- `internal/config/files_int_test.go`
- `internal/config/files_test.go`
- `internal/config/flux_plugins_test.go`
- `internal/config/helpers.go`
- `internal/config/k9plus_paths_test.go`
- `internal/config/plugin.go`
- `internal/dao/accessor.go`
- `internal/dao/flux.go`
- `internal/dao/flux_test.go`
- `internal/dao/registry.go`
- `internal/flux/resource.go`
- `internal/flux/resource_benchmark_test.go`
- `internal/flux/resource_test.go`
- `internal/model/cert_manager_test.go`
- `internal/model/cluster_info.go`
- `internal/model/colorer.go`
- `internal/model/colorer_test.go`
- `internal/model/flux_test.go`
- `internal/model/helpers.go`
- `internal/model/registry.go`
- `internal/model/release_identity_test.go`
- `internal/model/semver.go`
- `internal/model/semver_test.go`
- `internal/model1/flux_sort.go`
- `internal/model1/flux_sort_test.go`
- `internal/model1/performance_test.go`
- `internal/model1/row_clone_bench_test.go`
- `internal/model1/row_event.go`
- `internal/model1/row_event_internal_test.go`
- `internal/model1/row_event_test.go`
- `internal/model1/table_data.go`
- `internal/model1/table_data_test.go`
- `internal/model1/table_delete_bench_test.go`
- `internal/model1/table_delete_test.go`
- `internal/perf/benchmark.go`
- `internal/render/cert_manager.go`
- `internal/render/cert_manager_test.go`
- `internal/render/flux.go`
- `internal/render/flux_test.go`
- `internal/render/helpers.go`
- `internal/ui/config.go`
- `internal/ui/crumbs.go`
- `internal/ui/crumbs_test.go`
- `internal/ui/display_width.go`
- `internal/ui/display_width_test.go`
- `internal/ui/flash_test.go`
- `internal/ui/indicator.go`
- `internal/ui/logo.go`
- `internal/ui/logo_test.go`
- `internal/ui/menu.go`
- `internal/ui/menu_test.go`
- `internal/ui/modal_list.go`
- `internal/ui/modal_list_test.go`
- `internal/ui/padding.go`
- `internal/ui/padding_test.go`
- `internal/ui/performance_test.go`
- `internal/ui/splash.go`
- `internal/ui/table.go`
- `internal/ui/table_helper.go`
- `internal/ui/table_literal_test.go`
- `internal/view/actions.go`
- `internal/view/app.go`
- `internal/view/browser.go`
- `internal/view/cert_manager.go`
- `internal/view/cert_manager_test.go`
- `internal/view/clipboard.go`
- `internal/view/cluster_info.go`
- `internal/view/command.go`
- `internal/view/cronjob.go`
- `internal/view/cronjob_test.go`
- `internal/view/env.go`
- `internal/view/exec.go`
- `internal/view/flux.go`
- `internal/view/flux_test.go`
- `internal/view/registrar.go`
- `internal/watch/factory.go`
- `internal/watch/factory_test.go`
- `plugins/README.md`
- `plugins/cert-manager.yaml`
- `plugins/cloudnative-pg.yaml`
- `plugins/flux.yaml`
- `scripts/benchmark-performance.py`
- `scripts/capture-demo.py`
- `scripts/collect-licenses.py`
- `scripts/flux-inline-demo.py`
- `scripts/perf-demo.py`
- `scripts/test_collect_licenses.py`

## Native Cilium/Hubble integration

Added a native Relay gRPC client, per-context TLS configuration, peers and
conversation inspection, safe event detail, pod jumps, structured filtering,
reported coverage/loss, and stable frozen datasets. See docs/hubble.md for the
first increment's behavior, verification, and visibility limits.
