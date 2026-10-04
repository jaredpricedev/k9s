<!-- Modified for k9+; see NOTICE. -->

# Change and release review validation

The first TK06/TK07 delivery uses Go 1.25.8 and golangci-lint 2.6.2, matching
the repository toolchain and CI configuration. The implementation commit is
`5859dea3d9165519521112cba82272e21a23f210`; the capture source
`8b377c660b46c5c921608d70363fbf926a7ee34f` additionally labels the rollout
chrome as **Read coverage** so collection completeness cannot be mistaken for
controller completion.

| Check | Result |
| --- | --- |
| `go test -p 4 ./...` | Passed on the implementation revision |
| `go test -p 4 ./internal/review ./internal/view` | Passed after final comparison/collection changes |
| `golangci-lint run --concurrency=4` | Passed, zero issues with v2.6.2 |
| `go test -race -p 4 ./internal/review ./internal/view` | Passed after the isolated timing check described below |
| Narrow/wide rollout painting regression | Passed on the capture revision |
| Native terminal journeys | 18 checks passed at 80×24 and 120×34; provenance below |

The first race run overlapped compilation and lint and timed out in the existing
`TestWorkbenchWriterBurstUsesEntryAndByteBudgets` five-second drain check. Its
isolated race rerun passed in 4.606 seconds; the complete affected-package race
rerun then passed (`internal/review` 4.986 seconds, `internal/view` 34.239 seconds).
No log writer code or timeout was changed. This records a timing-sensitive
check, rather than discarding the initial result. The hosted race job now also
includes the new review domain, and the terminal job runs the new journeys.

The source parser tests exercise exact byte hashing, file stability, bounded
strict multi-document input, duplicate identities, cancellation, symlink
exclusion, safe errors and Secret identity-only retention. Comparison tests
cover omitted/defaulted fields, injected native named-list entries, mount/device
merge keys, equivalent quantities, explicit nulls, CRD list limits, literal
redaction markers and aggregate report bounds.

Collection and view tests exercise visible saved-workspace scope, exact-version
mapping, named reads, default namespace rules, labels/kinds, no Secret/LIST/write
requests, UID replacements, partial coverage, per-entry retained timestamps,
source-fixed refresh and abandoned callbacks. Rollout tests exercise current
generation observation, optional counts, controlling-owner UIDs, source caps,
unknown controller-selected revisions, exact retained templates and recovery
navigation without additional reads.

See [the native capture record](evidence/change-release-review-2026-10-04/README.md)
for the development binary and script hashes, actual terminal cells, visited
detail pages and request journals. Each width passed nine workflow checks with
49 requests, including 15 named Deployment/descendant resource reads. Startup
SelfSubjectAccessReview POSTs are permission probes; the reviewed workflows
submitted no workload mutations. No Secret or out-of-scope namespace read was
made, and the synthetic credential marker is absent from saved terminal text.

These results establish bounded synthetic behavior and the recorded toolchain
checks. They do not establish live-cluster controller coverage, server preview
fidelity, rollback execution, application availability or human operator
usability. Hosted CI status should be read from PR #41 for its current head;
captured source revisions and hashes remain historical evidence.

## Review UI follow-up

The #42/#44/#45 follow-up keeps both authored image and memory changes visible
on the first detail screen. `e` explicitly opens complete source, captured scope,
timestamps, live identity and ownership evidence; Esc returns to the same table.
Revision page navigation selects retained UIDs, while Ctrl-F/Ctrl-B scroll long
Recovery text without changing the pending revision choice.

The updated native journey passed **20 checks** at **80×24** and **120×34** against
the exact published PR #84 source `eff72a376d40ccb0ef92392282eaf9cc4ecfef5c`.
It checks first-screen values before opening Evidence, complete provenance after
opening Evidence, table return, source-fixed refresh, denied/recreated target
continuity, and original recovery-template retention. It audits every request
for workload mutations, Secret reads, scope and bounded descendant queries.
The synthetic fixture submitted no workload changes. The checks use the native
Ctrl-F text scroll action so PageDown cannot alter the revision under review.

Focused native SimulationScreen and semantic tests passed with Go 1.25.8:
`go test -ldflags=-w -p 1 ./internal/ui ./internal/view ./internal/review -run 'TestTask|TestDesired|TestRollout' -count=1`.
These include 120/80/60-column changed-value painting, the 40×12 task floor,
25-revision selection/resize/search, and explicit removed-UID selection state.
These follow-up checks retain the same synthetic and operator-study limits as
the original validation above.
