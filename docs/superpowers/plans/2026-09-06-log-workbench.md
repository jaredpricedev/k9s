# Log workbench implementation plan

Goal: implement and expose all approved log workbench capabilities in k9+.
Architecture: a pure internal/logstream package owns entries and transformations/storage; DAO owns Kubernetes collection; the existing Log view exposes a table workbench sharing its source and lifecycle.
Tech stack: Go 1.25.8, existing tview/tcell, Kubernetes client-go, standard library; optional installed ripgrep.
Spec: docs/superpowers/specs/2026-09-06-log-workbench.md.

## Global Constraints

- Keep raw data independent of TUI markup. Default render/copy/export/recording redaction is on; raw disk recording requires a separate explicit action.
- Bound memory, source concurrency, grouping, ordering and disk usage. Surface loss, errors and approximation to users.
- Filtering is client-side. Request runtime timestamps. Never imply complete historical logs or exact cross-node ordering.
- Stable selection must survive updates and filtering; preserve existing user shortcuts, document all new ones.
- Use an isolated feature branch. Do not alter unrelated playground resources or merge this branch automatically.
- Write meaningful tests before implementation, then run them. Toolchain is /opt/k9s-toolchain/go/bin/go locally. Remote lab tool can build/test if local dependencies are unavailable.

## Task 1: Entry processing, storage and profiles

Create internal/logstream as a pure package with no Kubernetes or UI dependency. Files should separate entry/parser, query, redaction, grouping/patterns/histogram, recording and profile concerns with corresponding tests.

- [ ] Define Source (context, namespace, pod, UID, container, generation), Entry (monotonic ID, runtime and application times, raw, format, level, message, fields, source, marker provenance, occurrences/original lines) and an engine holding a bounded ring. Publish a short API guide for downstream tasks at .superpowers/sdd/2026-09-06-log-workbench/core-api.md.
- [ ] Parse JSON using numeric fidelity and flatten/access dotted nested fields; strict logfmt with quoted escapes, per-entry fallback. Sample formats per source for first fifty records but never permanently disable parsing. Normalize level aliases and leave runtime/app times separate.
- [ ] Compile queries supporting conjunction of field comparisons (quoted values), level severity ordering, numbers, booleans/string equality, dotted paths; missing fields never match. Regex raw fallback and stacked negative substrings. Invalid syntax is an error. Tests include mixed structured/raw, malformed syntax and level/error/HTTP500 examples.
- [ ] Redact JWT/bearer/private PEM/AWS keys, including multiline content; sanitize terminal controls. Default safe transforms usable by render, snippet and recording independently. Tests ensure no escape/markup interpretation belongs in domain parser.
- [ ] Source-local multiline grouping bounded by 64 lines, 64KiB and 500ms idle; preserve all lines and flush on boundary/timeout/end. Exact consecutive collapse, pattern templates preserving severity/status, top patterns and originals; stable entry IDs and multiplicity. Bounded 250ms reorder buffer with max2048 entries, configurable small limits for tests. Histogram 60 one-second buckets, severity, count and entry target IDs.
- [ ] Recording uses 0600 files/0700 directory, capped default128MiB total per session in segments and bounded in-memory index, retention24h configurable. Search/paging reads disk without loading full history; resume parses safely and tolerates interrupted trailing record. Optional rg argument-safe raw search with Go fallback; typed query path uses parser. Recording redacts by default independent of render toggle; support explicit raw option and metadata flag. Export/snippet safe by default with context and loss/filter metadata. Expose errors to caller. Tests force segment eviction, resume, redaction, missing rg and malformed/oversized records.
- [ ] Profiles are scoped by context/namespace/label key/value or kind/name fallback, configurable label keys; JSON array team annotation plus local negative substrings, atomic writes, bounded rules and explanatory errors. No mutation of Kubernetes data.
- [ ] Run /opt/k9s-toolchain/go/bin/go test ./internal/logstream (and race if ready), self-review, commit only owned package files and write task report.

## Task 2: Kubernetes collection and model handoff

Own internal/dao logging files (log_item.go, log_options.go, pod.go, ds.go and workload selector callers), new log_follow.go and tests; internal/model/log.go only for new entry handoff and removing global cursor mutation. Consume the core Source/Entry contract documented by Task1.

- [ ] Enrich LogItem with actual raw bytes and source metadata while preserving existing render compatibility. Do not pass escaped bytes into parsing. Legacy display must remain escaped safely.
- [ ] Implement selector list/watch from typed client, relist/watch recovery, full LabelSelector matchExpressions, pod UID and container generation tracking, cap32 active streams. Return one multiplexed channel so newly discovered sources join existing model. Single pod mode detects container restarts too. Cancellation closes streams and unblocks reads/sends. Cursor per source with inclusive sinceTime boundary dedup, avoiding global +1s gaps; completed/head/previous streams must not loop forever.
- [ ] Source join/leave, rollout revision change, last termination and Event markers flow as typed metadata with source/time/provenance, bounded dedup. Events optional; forbidden errors visible once and logs continue. Source cap/drop/truncation visible. Use bounded reads for pathological lines. Replace flaky cancellation test with controlled reader/pipe.
- [ ] Expose a model entry subscription or snapshot callback that feeds workbench without parsing rendered text. Keep listener lifecycle race-safe, flush timer under continuously busy traffic and stopped callbacks safe. Profile scope/annotation metadata is supplied from selected workload and matching pod labels.
- [ ] Test using fake clients/watch/controlled streams: empty selector initial set to new pod, pod replacement, restart generation, replay timestamp boundary, watch relist, denied Events, cancellation blocked EOF/full queue and source cap. Run affected DAO/model tests and race checks; commit/report.

## Task 3: Interactive log workbench

Own internal/view/log*.go and new workbench view files/tests, internal/config/logger.go plus tests if settings are needed. Consume Tasks1/2 APIs and keep existing log routing working. Measured playground recording throughput is435entries/sec with per-entry durable checkpoints; also own the bounded Recorder.AppendBatch implementation and its tests in internal/logstream/recording.go for the correction below.

- [ ] Integrate a table-based workbench into existing Log view, enabled by default; retain access to raw legacy mode. Time/source/level/message columns use available terminal width. Stable selected entry ID; paused follow still collects boundedly. Escape all user text/neutralize ANSI. Parsing/mixed status and approximate order shown.
- [ ] Use existing / filter prompt for typed/regex filters and stacked -terms. Enter expands selected grouped entry with pretty fields/original lines in a readable full-width detail pane; do not conflict with active prompt Enter. Invalid filter leaves prior results. Toggle raw/structured, collapse, multiline and redaction with discoverable help/actions.
- [ ] Top patterns view with count, selectable drilldown to entries; source isolate/exclude/reset controls and per-source lane view (at least two selectable sources). Keep selection and scroll position stable while status/results change.
- [ ] Histogram severity sparkline, observed/visible counts; selectable bucket/jump-to-spike action. Retained history scope label. Detail/copy range uses stable IDs and reports evicted boundaries.
- [ ] Stack/save/reset local noise profile; read annotation and config app-label scope. Show active rule count and hidden count; provide a way to inspect active expressions.
- [ ] Start bounded safe recording automatically or explicit discoverable record action, make recording status/path/error visible. Implement history paging, disk search, resume session picker, safe export, and explicit raw-recording action separate from redaction toggle. Server timestamp jump uses sinceTime and labels available-server-history limitation. No hardcoded unavailable commands in help.
- [ ] Batch disk writes up to256 entries and4MiB with at most100ms flush delay, using one durable intent/checkpoint per batch while preserving interrupted-write redaction safety. Keep Append compatibility, test batch IDs/order/partial failure/resume, and make writer overflow/error visible. Keep collection/render independent of disk latency and drain on close. Bound the number of owned auto-created sessions as well as each session's size and retention. Recheck throughput on the playground.
- [ ] Copy selected line/marked range with metadata via existing clipboard fallback. Safe default even when display unredacted; use separately explicit raw action if provided. Test actions, entry identity preservation, filter errors, recording error visibility, safe copy/export and all new modes reachable.
- [ ] Run view/config targeted tests, compile binary, self-review and commit/report. Include a precise key map/API testing guide for playground verification.

## Task 4: End-to-end validation and documentation

- [ ] Add reproducible logging-demo manifests and user guide in docs/log-workbench.md (or existing docs conventions). Demo JSON/logfmt/raw, stack traces, repeated noise, synthetic redaction strings, replica skew, controlled errors and restart/rollout scripts. No actual credentials.
- [ ] Run go test ./... and focused race tests covering new pipeline, stream lifecycle and model. Fix concrete failures with regression tests.
- [ ] Deploy in namespace logging-demo in existing iximiuz play6a9da440f3ba453c20c37558, build/install candidate alongside stable binary, tmux UI checks of every new control, live filters, replica replacement/restart, recording/resume/export, cap/rotation behavior. Install as k9plus only after checks pass, retain previous binary backup.
- [ ] Review whole branch, publish feature branch and draft PR with concise rationale, behavior, tests and limitations. Give playground link, quick launch instructions, all eleven capabilities and remaining concrete limitations. Do not claim unseen test coverage or merge automatically.
