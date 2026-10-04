<!-- Modified for k9+; see NOTICE. -->

# k9plus roadmap

The toolkit provides saved scopes, daily findings, scoped inventory and clearer investigations; see [usage and limits](docs/daily-workspace.md). Change and release review adds named desired sources, server previews, ownership, outcomes and guarded recovery; see [behavior and limits](docs/change-release-review.md). The [daily Kubernetes toolkit roadmap](docs/toolkit-roadmap-2026-10-03.md) records delivery and remaining acceptance, and the tables below retain the earlier delivery history.

Keep the basic resource browser fast and familiar. Advanced tools open on demand;
no background discovery on ordinary resource refreshes. Preserve existing shortcuts,
context safety, selection, and the return path from investigations.

## Areas and priorities

| Area | Useful improvement | How to keep it lightweight | Status |
| --- | --- | --- | --- |
| Navigation and actions | Search available actions for the selected resource instead of memorizing shortcuts. | One action menu; frequently used shortcuts still work. | Shipped on master (PR #6) |
| Resource troubleshooting | Bring conditions, recent events, restarts and ownership together. | Open an inspector on demand. | Shipped on master (PR #6) |
| TLS and certificates | Show expiry, SANs, issuer, chain information and where a certificate is used; optional endpoint checks. | A certificate view with deeper inspection rather than more columns everywhere. | Shipped on master (PR #6) |
| Workload relationships | Jump between Deployment, Pods, Service, EndpointSlices and Ingress/Gateway. | Contextual links before attempting a large topology map. | Shipped on master ([PR #7](https://github.com/jaredpricedev/k9s/pull/7)); [usage and limits](https://github.com/jaredpricedev/k9s/blob/1978cb74caab53f74403e9e776d18bb2975e01e4/docs/workload-relationships.md) |
| Change visibility | Compare explicit observations A/B with source, time and recreated identity; review named desired sources. | Retained source identity, named reads and explicit limits; no inferred prune plan. | A/B and scoped desired-source/server preview shipped on master (#50, PR #91) |
| Capacity and autoscaling review | Review requests, limits, usage, admission constraints, HPA/VPA and node allocatable evidence for a captured selection. | Fixed bounded independent sources; explicit missing/stale inputs; no historical or aggregate scheduling claims. | Shipped on master (#59, PR #93) |
| Storage diagnosis and expansion | Review bind/attach/mount/resize stages, topology and CSI evidence; explicitly preview supported PVC increases. | Bounded read-only diagnosis; exact UID/version/binding rechecks and guarded size-only requests; capacity is not usage and acceptance is not completion. | Shipped on master (#57, PR #97) |
| Resource pressure | Investigate requests, limits, usage, OOM kills and scheduling evidence. | Workload-focused snapshots; missing usage is N/A and throttling stays unknown without counters. | Shipped on master; delivery below |
| Context safety | Clear production identity, convenient read-only mode and destination-aware action confirmations. | Persistent compact indicators and full F2 destination details. | Shipped on master; delivery below |
| Performance and interaction polish | Faster large lists, reliable cancellation, stable selection and consistent inspectors. | Improve existing workflows; retain bounded evidence and disclose measurements. | Shipped on master; delivery below |

## PR #6 delivery

| Area | Approach | Delivery |
| --- | --- | --- |
| Navigation/actions | Audit shortcuts, add a searchable action menu, preserve existing navigation. | `:actions` on a resource list; reuse registered actions and their existing safeguards. |
| Resource troubleshooting | Conditions and events first, then restarts, ownership and resource jumps. | `:troubleshoot`: conditions, owners, scoped workload pods, container states/restarts and UID-scoped retained events; refresh and related-resource jumps. |
| TLS | Begin with certificate parsing, expiry, SANs and resource links. Review trust validation, active probes and sensitive-data handling separately. | `:tls` on Secrets/Certificates/Ingresses/Gateways; public metadata, references, offline trust verification and explicit verified endpoint probes from k9plus. |

PR #6 completes the scoped increments for these three areas; see [usage and limits](docs/resource-investigation.md).
The later roadmap areas are delivered below. TLS revocation, mTLS client
identity, STARTTLS, in-pod probing and exhaustive cross-namespace consumer discovery
are outside this delivery. No certificate or network-policy changes are automatic.

Remaining Hubble features are tracked in [BACKLOG.md](BACKLOG.md). Network-policy
changes require explicit action and confirmation.

The daily-app correctness, visual foundation, investigation and evidence increments
are tracked in [issue #8](https://github.com/jaredpricedev/k9s/issues/8), with dependencies
and acceptance criteria in its linked issues. Proposed performance and usability goals
are targets until measured; fixture validation does not establish live-cluster coverage.

## Daily workspace delivery

All nineteen implementation issues (#9–#27) are shipped on the default `master`
branch. The delivery includes the workload relationships from PR #7 alongside
the daily-workspace increments and their core fixes. The links below retain the
review history; build and run the default branch with Go 1.25.8.

| Linked issue | Delivered behavior | Status | Delivery |
| --- | --- | --- | --- |
| #9 | Canonical selected resource identity, guarded synthetic views, nonzero fatal recovery | Merged on master | [PR #28](https://github.com/jaredpricedev/k9s/pull/28) |
| #10 | Validated drafts retain committed filters and resource selection | Merged on master | [PR #30](https://github.com/jaredpricedev/k9s/pull/30) |
| #11 | Explicit metrics visibility and correct memory trends | Merged on master | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
| #12 | Owned action-map snapshots and synchronized mutation | Merged on master | [PR #29](https://github.com/jaredpricedev/k9s/pull/29) |
| #13 | Persistent destination identity, access mode and full F2 details | Merged on master | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
| #14 | Default-branch installation, first-run tasks and feature matrix | Merged on master | [Validation PR #39](https://github.com/jaredpricedev/k9s/pull/39) |
| #15 | Asynchronous restart, scale and delete with captured destination and UID conditions | Merged on master | [Operations PR #33](https://github.com/jaredpricedev/k9s/pull/33) |
| #16 | Semantic and custom skins with readable selection and severity | Merged on master | [PR #31](https://github.com/jaredpricedev/k9s/pull/31) |
| #17 | Compact layouts preserve fault states, readiness and restarts | Merged on master | [PR #31](https://github.com/jaredpricedev/k9s/pull/31) |
| #18 | Frozen Hubble rows retain identity across ingestion and eviction | Merged on master | [Hubble PR #34](https://github.com/jaredpricedev/k9s/pull/34) |
| #19 | Bounded, paginated workload Hubble scope | Merged on master | [Hubble PR #34](https://github.com/jaredpricedev/k9s/pull/34) |
| #20 | Maintenance, CI and reproducible validation evidence | Merged on master | [Validation PR #39](https://github.com/jaredpricedev/k9s/pull/39) |
| #21 | Relationship UID checks, canceled navigation and retained source state | Merged on master | [PR #7](https://github.com/jaredpricedev/k9s/pull/7) |
| #22 | Shared action discovery/help implementation | Shared implementation merged; candidate PR #122 covers the wide-Coverage primary-hint criterion; representative-operator evaluation remains open | [PR #32](https://github.com/jaredpricedev/k9s/pull/32), [responsive refinements PR #90](https://github.com/jaredpricedev/k9s/pull/90), [PR #122](https://github.com/jaredpricedev/k9s/pull/122) |
| #23 | Fault-first investigation, stream modes and responsive evidence | Implementation merged; performance journey measured; representative-operator evaluation remains open | [PR #32](https://github.com/jaredpricedev/k9s/pull/32), [responsive refinements PR #90](https://github.com/jaredpricedev/k9s/pull/90) |
| #24 | Explicit selected-only capability checks and verified Relay readiness | Merged on master | [Diagnostics PR #36](https://github.com/jaredpricedev/k9s/pull/36) |
| #25 | Immutable A/B observations with identity, noise controls and sensitive-content limits | Merged on master | [Comparison PR #35](https://github.com/jaredpricedev/k9s/pull/35) |
| #26 | Requests, limits, optional usage, last OOM and bounded scheduling evidence | Merged on master | [Pressure PR #37](https://github.com/jaredpricedev/k9s/pull/37) |
| #27 | Bounded reviewed JSON/Markdown evidence and offline import | Merged on master | [Evidence PR #38](https://github.com/jaredpricedev/k9s/pull/38) |

The core fixes [#28](https://github.com/jaredpricedev/k9s/pull/28),
[#29](https://github.com/jaredpricedev/k9s/pull/29) and
[#30](https://github.com/jaredpricedev/k9s/pull/30), and delivery PRs
[#31](https://github.com/jaredpricedev/k9s/pull/31) through
[#39](https://github.com/jaredpricedev/k9s/pull/39), are included together with
[PR #7](https://github.com/jaredpricedev/k9s/pull/7). Source identity checks and
namespace return ownership keep related-resource navigation coherent with the
persistent destination header, including after later user navigation.

## Validation and remaining work

See [dated validation](docs/validation-2026-10-04.md) for actual PTY, race and
disposable Kubernetes coverage. Its captured source revisions, binary hashes and
CI snapshot remain historical evidence; they do not establish passing hosted CI
for a later merge revision.

A controlled 10,000-Pod, 20-query, 120×34 actual-CLI journey measured median
**38.685 ms / p95 58.557 ms** against an isolated baseline of 123.475 / 231.736 ms.
That meets the proposed 100 ms p95 target for this measured journey only. The
integrated publication-head sample measured 40.528 / 49.108 ms, but may have
overlapped a linker and is marked as potentially CPU-confounded. Broad or inverse
queries can still copy all rows; this is not a general latency bound. See the
[performance evidence](https://github.com/jaredpricedev/k9s/issues/8#issuecomment-5983095681).

Implementation for the shared action/help and responsive presentation foundations
is merged, but #22 and #23 remain open for representative-operator evaluation.
Automated fixtures do not measure learnability or task success. UI acceptance
candidate `c0e5ff82f5f965a2e630a5435795bc21d470da91` has tree
`d9cff84708c0b845f3bd5fae9bde388dbad02156` and is based on master
`d55c090487a72cf09a89a687bae6d0c3c70765ec`; it contains all 33 core
implementation scopes. PR #121 is merged; this tree is the PR #122 candidate.
Thirty-one of the original 41 delivery issues are closed
with merged work. The ten remaining delivery issues are #22/#23 human evaluation
and optional #65–#72; #69 is deferred because the user has no current on-prem
cost-review need. Tracker #8 remains open (11 open issues total). Core #53
shipped through [PR #118](https://github.com/jaredpricedev/k9s/pull/118). Seven
optional native foundations (#65–#68, #70–#72) are implemented in this source;
their demand, live compatibility and broader-provider acceptance gates remain.
PR #116 includes the #71 operator and #72 dependency foundations. UI acceptance
fixes and tests from PR #121 are merged in the current master snapshot. The PR #122
candidate source fixes the wide-Coverage primary-hint gap. Automated native tests
cover keyboard-reachable Help labels/reasons and listener lifecycle, contextual
action dispatch and hints, debounced Details search ownership, and investigation
viewport/export/search/scroll/draft/return behavior. They do not replace the
#22/#23 human studies, which remain unrun. Live
Relay/Gateway and other provider compatibility remain separate coverage gaps.
Scoped desired-source/server-preview and guarded Deployment recovery are delivered;
exhaustive prune fidelity and deeper protocol/topology features remain outside their
current bounds.
