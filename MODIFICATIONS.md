<!-- Modified for k9+; see NOTICE. -->

# k9+ modification notice

k9+ is an independent, modified distribution of [k9s](https://github.com/derailed/k9s).
Original copyright and license notices are retained. Modifications and additions
are provided under Apache-2.0; see LICENSE, COPYING, NOTICE and
[licensing](docs/licensing.md). The upstream comparison point is
`84852e6e47ae830b30c921ffa5838443387e096e`.

This notice inventories the accumulated fork and the daily-workspace increments
tracked by [issue #8](https://github.com/jaredpricedev/k9s/issues/8). It includes the
workload-relationships feature from original PR #7 and its integration with the
selected resource identity, retained observations and destination ownership.
[ROADMAP.md](ROADMAP.md) links the delivered features and their review history.

Source comments identify modified and added Go files; Git history records each
increment. The [maintenance guide](docs/maintenance.md) documents fork-owned
boundaries and upstream synchronization. The internal Go module path, compatible
configuration keys, schema names and Kubernetes annotation conventions remain
from k9s for interoperability and provenance, without implying upstream endorsement.

## Distribution and established workflows

- The application identity and distribution use k9plus paths, release naming,
  migration guidance and retained third-party license collection. The five-row,
  32-column Unicode block `[k9+]` wordmark shares glyphs across the terminal header,
  splash and CLI, with skin-aware colors.
- Native Flux views combine resource status with references and asynchronous,
  explicitly confirmed reconcile and suspend/resume operations. Native
  cert-manager views retain public certificate metadata, condition evidence,
  related references and namespace-scoped discovery.
- Resource investigations combine status, conditions, container state, retained
  events and ownership. TLS inspection adds public certificate parsing, named
  trust sources and explicit verified endpoint probes from the k9plus machine.
- The log workbench adds bounded entries, structured and text queries, source
  selection, display freeze, grouping, recording and explicit exports. Recognizable
  credential text and terminal controls pass through the shared log protections.
- Native Cilium/Hubble uses Relay gRPC with per-context verified TLS configuration,
  peer and conversation inspection, structured filters, event detail, pod jumps,
  coverage/loss notices and frozen display snapshots.
- Earlier large-list work improves table cloning, event application and Flux
  ordering. Reproducible benchmark and PTY scripts retain their capture inputs
  and source identity. Historical recordings and benchmarks preserve their
  original bytes and labels rather than being rewritten as current evidence.

## Daily-workspace increments

The toolkit's saved daily desk adds private, versioned scopes with explicit
context, namespaces, kinds and selectors; scoped inventory, a current-state
findings queue, pins, saved searches and coverage. Its readers use bounded
namespace requests and retain captured UIDs. Compact investigations use typed
facts and separate overview, containers, events, resources and full evidence;
comparison and pressure place useful changes and budgets first. Connection
checks explicitly rebuild diagnostic clients without changing the viewing
destination. See [usage and limits](docs/daily-workspace.md) and the
[broader toolkit roadmap](docs/toolkit-roadmap-2026-10-03.md). Native fixture
captures and test provenance are retained in the
[toolkit validation record](docs/daily-workspace-validation-2026-10-04.md).

| Boundary | Modification and resulting behavior |
| --- | --- |
| Selected resource identity | Capture context, native GVR, namespace, name and available UID once. Combined Flux rows resolve to their native resource; synthetic and unavailable selections explain their limitation. Known-UID replacements are rejected, and recovered fatal exits return nonzero. |
| Resource filters and key bindings | Validate query drafts, preserve the last committed rows and selection on invalid input, retain valid empty results, and restore all rows only on explicit clear. Action-map readers own snapshots and merge/clear operations hold their locks. |
| Metrics and destination identity | Share explicit available, unavailable, denied, stale and not-configured sample states across headers, Pulse and resource rows. Missing usage stays N/A; memory trends compare memory samples. Compact chrome keeps context, namespace and access mode visible through messages and prompts. |
| Presentation and themes | Use shared semantic health, severity, identity, focus and action colors, with custom-color fallback and stock, high-contrast and monochrome skins. Narrow tables preserve useful fault status, readiness and restarts; runtime skin changes also update dialog styles. |
| Operations and client lifetime | Move restart, scale and delete requests off UI callbacks. Pin immutable client configuration and source handles, validate captured identities, and use UID/resource-version conditions for writes. Cancellation stops waiting work; API acceptance and controller completion remain distinct. |
| Action discovery and return navigation | Use stable action metadata for searchable discovery and help across resource lists, Pulse, logs, Hubble and inspectors. Discovery leaves the owning stream active; unavailable actions retain their reason and existing fast keys. Inspectors retain successful evidence, accepted searches, highlighted matches and scroll position when refreshing, returning or changing skins. Cross-namespace returns restore the source viewing destination only while the jump owns it; later context or namespace choices invalidate that ownership, including reactive disk edits and choices that return to the earlier value. Trusted nested returns preserve only the ancestor ownership captured before their jump. |
| Workload relationships | Keep the original PR #7 implementation: namespace-scoped selector and configuration links between workloads, Pods, Services, EndpointSlices and Ingress/Gateway resources. Preserve link reasons and observed UIDs, reject known source or destination replacements, retain the target UID in its native list, and reject closed, expired or superseded navigation. The anchored row uses Inspector for UID-checked reads; live YAML, Describe and kubectl Edit require explicitly reopening its native list. Reference and endpoint evidence does not establish traffic or health. |
| Fault evidence | Order status/reason, priority conditions, container restarts and last termination, workload evidence and UID-scoped retained events before owners. Preserve full messages; compact message display is reversible without another API request. Failed refreshes keep the previous successful snapshot with a visible failure notice. |
| Hubble performance and scope | Reuse unchanged frozen data instead of cloning and rebuilding it each tick. Bound workload Pod resolution using namespace, selector and pagination, pin API handles, and reject identity changes or late navigation results. Visibility and truncation remain explicit. |
| Capability diagnostics | Run only an explicitly selected API, metrics, Flux, cert-manager or Hubble readiness check. Present missing, denied, stale and unavailable prerequisites with useful next steps; no port-forward starts automatically. |
| Observation comparison | Keep baseline A immutable; capture B explicitly with separate source, time and UID labels. Mark recreated identities and unavailable inputs. API bookkeeping normalization is reversible without another read; Secret bodies and credential-shaped fields are excluded from the shared safe projection. |
| Resource pressure | Inspect configured requests/limits, optional metrics usage, OOM/last termination and UID-scoped scheduling events. Keep init phases, restartable sidecars, Pod-level budgets and overhead distinct. Missing or stale usage remains N/A; throttling remains unknown without its counters. |
| Capacity and autoscaling review | Add a captured read-only capacity workspace with effective Pod requests, scheduling reservation, reported limits, fresh usage coverage, quota/LimitRange, HPA inputs, optional VPA recommendations and per-node allocatable evidence. Bounded independent reads preserve partial/denied sources; history remains not configured and aggregate CPU never becomes a scheduling verdict. |
| Storage diagnosis and expansion | Join bounded Pod/PVC/PV/class/CSI/attachment/UID-event evidence without inferring usage, mounting or completed resize. Preview one explicitly supported increase, freshly recheck captured UID/version/binding and submit a conditional size-only patch through retained guarded-operation receipts. Read-only mode blocks submission; API acceptance remains separate from controller/filesystem progress. |
| Portable evidence | Preview explicitly selected live evidence or retained comparison/inspection observations before saving. JSON and Markdown retain identity, source, time and completeness. Offline import never refreshes a resource or contacts the recorded context; exports create a new 0600 file without overwriting. Bundle, field and preview limits are explicit, and stopped or replaced forms cannot act on abandoned evidence. |
| Validation and maintenance | Add targeted regressions, race checks, protected disposable-cluster integration tests, actual PTY journeys, reproducible frozen-data benchmarks and capture provenance. Document first-run tasks, capability limits, domain vocabulary and fork maintenance responsibilities. |

See [inspection](docs/inspection.md), [operations](docs/operations.md),
[capabilities](docs/capabilities.md), [comparison](docs/resource-comparison.md),
[pressure](docs/pressure.md), [capacity review](docs/capacity-review.md), [storage diagnosis](docs/storage-review.md), [evidence bundles](docs/evidence-bundles.md) and
[dated validation](docs/validation-2026-10-04.md) for behavior, safeguards and
verification scope. Heuristic redaction is not a confidentiality guarantee.
Fixtures, historical live checks and current measurements are identified separately;
automated captures do not establish human operator usability or universal latency.

## Changed files relative to upstream

The change/release increment adds `internal/review` for explicit local source
identity, bounded authored-field comparison and typed native Deployment rollout
evidence. Native terminal views expose source-fixed refresh, scoped named reads,
UID-owned revisions/Pods and retained recovery previews without submitting
mutations. See [usage and fidelity](docs/change-release-review.md).

The following inventory compares the complete accumulated application tree with
upstream
`84852e6e47ae830b30c921ffa5838443387e096e`, including additions, modifications,
removals, attribution-only updates and final filter, UI, clean-build, CLI and
validation evidence, plus the original relationship feature and its navigation
integration. Files are grouped by ownership boundary; removed paths are marked
explicitly. Generated THIRD_PARTY_LICENSES and ignored build outputs are
assembled separately for distributions and are not source inventory entries.

### Project, build and release files

- `.dockerignore`
- `.gitattributes`
- `.github/FUNDING.yml` (removed)
- `.github/ISSUE_TEMPLATE/bug_report.md`
- `.github/ISSUE_TEMPLATE/feature_request.md`
- `.github/workflows/lint.yml`
- `.github/workflows/test.yml`
- `.gitignore`
- `.goreleaser.yml`
- `Dockerfile`
- `Makefile`
- `NOTICE`
- `docs/capacity-review-validation-2026-10-04.md`
- `docs/capacity-review.md`
- `docs/storage-review-validation-2026-10-04.md`
- `docs/storage-review.md`
- `go.mod`
- `go.sum`

### Documentation and design records

- `BACKLOG.md`
- `CONTEXT.md`
- `MODIFICATIONS.md`
- `README.md`
- `ROADMAP.md`
- `docs/adr/0001-safe-observations-and-evidence.md`
- `docs/capabilities.md`
- `docs/certificates.md`
- `docs/evidence-bundles.md`
- `docs/examples/logging-demo-actions.sh`
- `docs/examples/logging-demo.yaml`
- `docs/filter-performance-2026-10-04.md`
- `docs/first-run.md`
- `docs/flux-status-benchmark-2026-09-06.txt`
- `docs/flux.md`
- `docs/hubble-performance-2026-10-04.md`
- `docs/hubble-verification.md`
- `docs/hubble.md`
- `docs/inspection.md`
- `docs/licensing.md`
- `docs/log-workbench.md`
- `docs/maintenance.md`
- `docs/migration.md`
- `docs/operations.md`
- `docs/performance-2026-09-06.md`
- `docs/presentation.md`
- `docs/pressure.md`
- `docs/resource-comparison.md`
- `docs/resource-investigation.md`
- `docs/review-2026-09-06.md`
- `docs/superpowers/plans/2026-09-06-certificate-polish.md`
- `docs/superpowers/plans/2026-09-06-flux-review.md`
- `docs/superpowers/plans/2026-09-06-log-workbench.md`
- `docs/superpowers/plans/2026-09-06-performance-pass.md`
- `docs/superpowers/plans/2026-09-07-native-hubble.md`
- `docs/superpowers/plans/2026-09-07-resource-investigation.md`
- `docs/superpowers/specs/2026-09-06-flux-review-design.md`
- `docs/superpowers/specs/2026-09-06-log-workbench.md`
- `docs/validation-2026-10-04.md`
- `docs/workload-relationships.md`

### Command-line entry points

- `cmd/branding_test.go`
- `cmd/fatal_test.go`
- `cmd/info.go`
- `cmd/root.go`
- `cmd/version.go`

### Resource access, observations and models

- `internal/capacity/autoscaling.go`
- `internal/capacity/collect.go`
- `internal/capacity/collect_test.go`
- `internal/capacity/deadline_test.go`
- `internal/capacity/render.go`
- `internal/capacity/resources.go`
- `internal/capacity/resources_test.go`
- `internal/capacity/types.go`
- `internal/certmanager/resource.go`
- `internal/certmanager/resource_test.go`
- `internal/client/client.go`
- `internal/client/config_snapshot.go`
- `internal/client/config_snapshot_test.go`
- `internal/client/gvrs.go`
- `internal/client/metric_sample.go`
- `internal/client/metric_sample_test.go`
- `internal/client/metrics.go`
- `internal/dao/accessor.go`
- `internal/dao/dp.go`
- `internal/dao/ds.go`
- `internal/dao/flux.go`
- `internal/dao/flux_test.go`
- `internal/dao/helm_chart.go`
- `internal/dao/helm_history.go`
- `internal/dao/helm_operation_test.go`
- `internal/dao/job.go`
- `internal/dao/log_backpressure_test.go`
- `internal/dao/log_cursor.go`
- `internal/dao/log_follow.go`
- `internal/dao/log_follow_review_test.go`
- `internal/dao/log_follow_test.go`
- `internal/dao/log_item.go`
- `internal/dao/log_options.go`
- `internal/dao/log_read.go`
- `internal/dao/pod.go`
- `internal/dao/readlogs_test.go`
- `internal/dao/recorder.go`
- `internal/dao/recorder_sample_test.go`
- `internal/dao/registry.go`
- `internal/dao/sts.go`
- `internal/dao/svc.go`
- `internal/flux/resource.go`
- `internal/flux/resource_benchmark_test.go`
- `internal/flux/resource_test.go`
- `internal/hubble/client.go`
- `internal/hubble/client_test.go`
- `internal/hubble/filter.go`
- `internal/hubble/model.go`
- `internal/hubble/model_test.go`
- `internal/hubble/readiness.go`
- `internal/hubble/relay_integration_test.go`
- `internal/hubble/tls_test.go`
- `internal/inspect/bundle.go`
- `internal/inspect/bundle_test.go`
- `internal/inspect/certificate.go`
- `internal/inspect/certificate_test.go`
- `internal/inspect/resource_budgets.go`
- `internal/inspect/resource_budgets_test.go`
- `internal/inspect/snapshot.go`
- `internal/inspect/snapshot_test.go`
- `internal/inspect/verify.go`
- `internal/inspect/verify_test.go`
- `internal/logstream/engine.go`
- `internal/logstream/engine_test.go`
- `internal/logstream/entry.go`
- `internal/logstream/entry_test.go`
- `internal/logstream/export.go`
- `internal/logstream/pattern.go`
- `internal/logstream/profile.go`
- `internal/logstream/query.go`
- `internal/logstream/recording.go`
- `internal/logstream/recording_batch_test.go`
- `internal/logstream/recording_test.go`
- `internal/logstream/redaction.go`
- `internal/model/cert_manager_test.go`
- `internal/model/cluster.go`
- `internal/model/cluster_info.go`
- `internal/model/cluster_info_lifecycle_test.go`
- `internal/model/cluster_metrics_test.go`
- `internal/model/cmd_buff.go`
- `internal/model/cmd_buff_listener_test.go`
- `internal/model/colorer.go`
- `internal/model/colorer_test.go`
- `internal/model/flash_test.go`
- `internal/model/flux_test.go`
- `internal/model/helpers.go`
- `internal/model/log.go`
- `internal/model/log_entries_test.go`
- `internal/model/log_int_test.go`
- `internal/model/registry.go`
- `internal/model/release_identity_test.go`
- `internal/model/semver.go`
- `internal/model/semver_test.go`
- `internal/model1/flux_sort.go`
- `internal/model1/flux_sort_test.go`
- `internal/model1/performance_test.go`
- `internal/model1/resource_filter_test.go`
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
- `internal/render/container.go`
- `internal/render/flux.go`
- `internal/render/flux_test.go`
- `internal/render/helpers.go`
- `internal/render/metric_availability.go`
- `internal/render/metric_availability_test.go`
- `internal/render/node.go`
- `internal/render/pod.go`
- `internal/render/pod_test.go`
- `internal/view/capacity_review.go`
- `internal/view/capacity_review_test.go`
- `internal/storage/collect.go`
- `internal/storage/expand.go`
- `internal/storage/join.go`
- `internal/storage/project.go`
- `internal/storage/render.go`
- `internal/storage/stages.go`
- `internal/storage/storage_test.go`
- `internal/storage/transport_test.go`
- `internal/storage/types.go`
- `internal/view/storage_expand_form.go`
- `internal/view/storage_expand_operation.go`
- `internal/view/storage_expand_operation_test.go`
- `internal/view/storage_review.go`
- `internal/view/storage_review_test.go`
- `internal/watch/factory.go`
- `internal/watch/factory_test.go`

### Configuration, skins and plugins

- `internal/config/alias.go`
- `internal/config/alias_test.go`
- `internal/config/cert_manager_plugins_test.go`
- `internal/config/cnpg_plugins_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/config/data/context.go`
- `internal/config/data/helpers.go`
- `internal/config/data/helpers_test.go`
- `internal/config/files.go`
- `internal/config/files_int_test.go`
- `internal/config/files_test.go`
- `internal/config/flux_plugins_test.go`
- `internal/config/helpers.go`
- `internal/config/json/schemas/context.json`
- `internal/config/json/schemas/k9s.json`
- `internal/config/json/schemas/skin.json`
- `internal/config/k9plus_paths_test.go`
- `internal/config/logger.go`
- `internal/config/logger_test.go`
- `internal/config/plugin.go`
- `internal/config/semantic.go`
- `internal/config/semantic_test.go`
- `internal/config/styles.go`
- `internal/config/styles_int_test.go`
- `internal/config/styles_test.go`
- `internal/config/templates/stock-skin.yaml`
- `internal/config/testdata/configs/default.yaml`
- `internal/config/testdata/configs/expected.yaml`
- `internal/config/testdata/configs/k9s.yaml`
- `internal/config/types.go`
- `plugins/README.md`
- `plugins/cert-manager.yaml`
- `plugins/cloudnative-pg.yaml`
- `plugins/flux.yaml`
- `scripts/probe-capacity.py`
- `scripts/probe-storage.py`
- `skins/high-contrast.yaml`
- `skins/monochrome.yaml`

### Terminal interface and workflows

- `internal/ui/action.go`
- `internal/ui/action_metadata.go`
- `internal/ui/action_metadata_test.go`
- `internal/ui/action_test.go`
- `internal/ui/config.go`
- `internal/ui/crumbs.go`
- `internal/ui/crumbs_test.go`
- `internal/ui/dialog/confirm.go`
- `internal/ui/dialog/delete.go`
- `internal/ui/dialog/error.go`
- `internal/ui/dialog/message.go`
- `internal/ui/dialog/plugin_inputs.go`
- `internal/ui/dialog/prompt.go`
- `internal/ui/dialog/restart.go`
- `internal/ui/dialog/selection.go`
- `internal/ui/dialog/style.go`
- `internal/ui/dialog/style_test.go`
- `internal/ui/dialog/transfer.go`
- `internal/ui/display_width.go`
- `internal/ui/display_width_test.go`
- `internal/ui/flash.go`
- `internal/ui/flash_test.go`
- `internal/ui/indicator.go`
- `internal/ui/indicator_identity_test.go`
- `internal/ui/indicator_test.go`
- `internal/ui/logo.go`
- `internal/ui/logo_test.go`
- `internal/ui/menu.go`
- `internal/ui/menu_test.go`
- `internal/ui/message_modal.go`
- `internal/ui/message_modal_test.go`
- `internal/ui/modal_list.go`
- `internal/ui/modal_list_test.go`
- `internal/ui/padding.go`
- `internal/ui/padding_test.go`
- `internal/ui/pages.go`
- `internal/ui/performance_test.go`
- `internal/ui/presentation_capture_test.go`
- `internal/ui/prompt.go`
- `internal/ui/prompt_test.go`
- `internal/ui/select_table.go`
- `internal/ui/splash.go`
- `internal/ui/table.go`
- `internal/ui/table_filter_test.go`
- `internal/ui/table_helper.go`
- `internal/ui/table_layout.go`
- `internal/ui/table_layout_test.go`
- `internal/ui/table_literal_test.go`
- `internal/ui/table_test.go`
- `internal/ui/types.go`
- `internal/view/action_catalog.go`
- `internal/view/action_catalog_test.go`
- `internal/view/action_palette.go`
- `internal/view/action_registry_fixture_test.go`
- `internal/view/actions.go`
- `internal/view/alias_test.go`
- `internal/view/app.go`
- `internal/view/app_test.go`
- `internal/view/browser.go`
- `internal/view/browser_filter_queue_test.go`
- `internal/view/capability_diagnostics.go`
- `internal/view/capability_diagnostics_test.go`
- `internal/view/cert_manager.go`
- `internal/view/cert_manager_test.go`
- `internal/view/clipboard.go`
- `internal/view/cluster_info.go`
- `internal/view/cluster_info_test.go`
- `internal/view/cm_test.go`
- `internal/view/command.go`
- `internal/view/container_test.go`
- `internal/view/context_test.go`
- `internal/view/cronjob.go`
- `internal/view/cronjob_test.go`
- `internal/view/details.go`
- `internal/view/dir_test.go`
- `internal/view/dp_test.go`
- `internal/view/ds_test.go`
- `internal/view/env.go`
- `internal/view/evidence_bundle.go`
- `internal/view/evidence_bundle_test.go`
- `internal/view/exec.go`
- `internal/view/flux.go`
- `internal/view/flux_test.go`
- `internal/view/header_layout_test.go`
- `internal/view/help.go`
- `internal/view/help_test.go`
- `internal/view/hubble.go`
- `internal/view/hubble_actions.go`
- `internal/view/hubble_detail.go`
- `internal/view/hubble_resource.go`
- `internal/view/hubble_resource_test.go`
- `internal/view/hubble_table.go`
- `internal/view/hubble_terminal_test.go`
- `internal/view/hubble_test.go`
- `internal/view/inspection_connection.go`
- `internal/view/inspection_evidence_test.go`
- `internal/view/inspection_links.go`
- `internal/view/inspection_links_test.go`
- `internal/view/inspection_tls.go`
- `internal/view/inspection_tls_form.go`
- `internal/view/live_view.go`
- `internal/view/log.go`
- `internal/view/log_indicator.go`
- `internal/view/log_indicator_test.go`
- `internal/view/log_int_test.go`
- `internal/view/log_recording.go`
- `internal/view/log_recording_lifecycle.go`
- `internal/view/log_recording_start.go`
- `internal/view/log_workbench.go`
- `internal/view/log_workbench_history.go`
- `internal/view/log_workbench_query.go`
- `internal/view/log_workbench_render.go`
- `internal/view/log_workbench_state.go`
- `internal/view/log_workbench_style.go`
- `internal/view/log_workbench_test.go`
- `internal/view/logger.go`
- `internal/view/native_relationship_guard.go`
- `internal/view/native_relationship_guard_test.go`
- `internal/view/ns_test.go`
- `internal/view/operation_runner.go`
- `internal/view/operation_runner_test.go`
- `internal/view/operations_integration_test.go`
- `internal/view/pf_test.go`
- `internal/view/pod_test.go`
- `internal/view/pressure.go`
- `internal/view/pressure_test.go`
- `internal/view/priorityclass_test.go`
- `internal/view/pulse.go`
- `internal/view/pulse_metrics_test.go`
- `internal/view/pvc_test.go`
- `internal/view/rbac_test.go`
- `internal/view/reference_test.go`
- `internal/view/registrar.go`
- `internal/view/relationship_identity_test.go`
- `internal/view/relationship_navigation_test.go`
- `internal/view/resource_comparison.go`
- `internal/view/resource_comparison_test.go`
- `internal/view/resource_inspector.go`
- `internal/view/resource_inspector_test.go`
- `internal/view/restart_extender.go`
- `internal/view/scale_extender.go`
- `internal/view/screen_dump_test.go`
- `internal/view/secret_test.go`
- `internal/view/selected_resource.go`
- `internal/view/selected_resource_test.go`
- `internal/view/sts_test.go`
- `internal/view/svc_test.go`
- `internal/view/table.go`
- `internal/view/table_filter.go`
- `internal/view/table_filter_test.go`
- `internal/view/workload_relationships.go`
- `internal/view/workload_relationships_test.go`
- `internal/view/yaml_test.go`

### Reproduction and license tooling

- `scripts/benchmark-performance.py`
- `scripts/capture-demo.py`
- `scripts/collect-licenses.py`
- `scripts/flux-inline-demo.py`
- `scripts/hubble-latency.py`
- `scripts/hubble/README.md`
- `scripts/hubble/fixtures.yaml`
- `scripts/perf-demo.py`
- `scripts/regression-journeys.py`
- `scripts/render-presentation.py`
- `scripts/skin-journeys.py`
- `scripts/terminal-requirements.txt`
- `scripts/test_collect_licenses.py`

### Recorded screenshots, traces and performance evidence

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
- `docs/evidence/filter-2026-10-04/before-pods-10000.png`
- `docs/evidence/filter-2026-10-04/before-pods-10000.txt`
- `docs/evidence/filter-2026-10-04/before.json`
- `docs/evidence/filter-2026-10-04/final-pending-restart.png`
- `docs/evidence/filter-2026-10-04/final-pending-restart.txt`
- `docs/evidence/filter-2026-10-04/final-pods-10000.png`
- `docs/evidence/filter-2026-10-04/final-pods-10000.txt`
- `docs/evidence/filter-2026-10-04/final.json`
- `docs/evidence/filter-2026-10-04/manifest.json`
- `docs/evidence/hubble-2026-10-04/before-benchmarks.txt`
- `docs/evidence/hubble-2026-10-04/before-build-info.txt`
- `docs/evidence/hubble-2026-10-04/before-latency.json`
- `docs/evidence/hubble-2026-10-04/final-benchmarks.txt`
- `docs/evidence/hubble-2026-10-04/final-build-info.txt`
- `docs/evidence/hubble-2026-10-04/final-latency.json`
- `docs/evidence/hubble-2026-10-04/provenance.json`
- `docs/evidence/merge-2026-10-04/README.md`
- `docs/evidence/merge-2026-10-04/lint-followup.txt`
- `docs/evidence/merge-2026-10-04/lint.txt`
- `docs/evidence/merge-2026-10-04/ordinary-followup.txt`
- `docs/evidence/merge-2026-10-04/ordinary.txt`
- `docs/evidence/merge-2026-10-04/publication-50.txt`
- `docs/evidence/merge-2026-10-04/race-followup.txt`
- `docs/evidence/merge-2026-10-04/race.txt`
- `docs/evidence/ui-2026-10-04/README.md`
- `docs/evidence/ui-2026-10-04/manifest.json`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-256-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-256-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-256.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-256.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-true-color-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-true-color-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-true-color.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-high-contrast-true-color.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-256-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-256-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-256.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-256.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-true-color-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-true-color-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-true-color.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-monochrome-true-color.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-256-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-256-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-256.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-256.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-true-color-destination.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-true-color-destination.txt`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-true-color.png`
- `docs/evidence/ui-2026-10-04/pods-80x24-stock-true-color.txt`
- `docs/evidence/validation-2026-10-04/README.md`
- `docs/evidence/validation-2026-10-04/clean-build-info.txt`
- `docs/evidence/validation-2026-10-04/clean-build.json`
- `docs/evidence/validation-2026-10-04/cli-help.txt`
- `docs/evidence/validation-2026-10-04/cli-info.txt`
- `docs/evidence/validation-2026-10-04/cli-version.txt`
- `docs/evidence/validation-2026-10-04/comparison.txt`
- `docs/evidence/validation-2026-10-04/diagnostics.txt`
- `docs/evidence/validation-2026-10-04/evidence.txt`
- `docs/evidence/validation-2026-10-04/final-journeys.json`
- `docs/evidence/validation-2026-10-04/go-cgo0.txt`
- `docs/evidence/validation-2026-10-04/go-race.txt`
- `docs/evidence/validation-2026-10-04/hubble.txt`
- `docs/evidence/validation-2026-10-04/kubernetes-1.34.txt`
- `docs/evidence/validation-2026-10-04/license-bundle.txt`
- `docs/evidence/validation-2026-10-04/license-tests.txt`
- `docs/evidence/validation-2026-10-04/lint-edit-test.txt`
- `docs/evidence/validation-2026-10-04/lint.txt`
- `docs/evidence/validation-2026-10-04/operations.txt`
- `docs/evidence/validation-2026-10-04/pressure.txt`
- `docs/evidence/validation-2026-10-04/review-pr-status.json`
- `docs/evidence/validation-2026-10-04/workspace-followup-race.txt`
- `docs/evidence/validation-2026-10-04/workspace-followup.txt`
- `docs/evidence/validation-2026-10-04/workspace.txt`
