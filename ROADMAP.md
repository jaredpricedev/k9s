<!-- Modified for k9+; see NOTICE. -->

# k9plus roadmap

The next stage is documented in the [daily Kubernetes toolkit roadmap](docs/toolkit-roadmap-2026-10-03.md). Its first horizon provides clearer investigation overviews, saved scopes, daily findings, scoped inventory and connection checks; see [usage and limits](docs/daily-workspace.md). The next increment begins change and release review with local authored intent and native Deployment evidence; see [behavior and remaining work](docs/change-release-review.md). The tables below retain the earlier delivery history.

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
| Change visibility | Compare explicit observations A/B with source, time and recreated identity; review local authored manifest fields. | Retained source identity, named reads and explicit limits; no inferred prune plan. | A/B shipped on master; local desired review in this increment |
| Capacity and autoscaling review | Review requests, limits, usage, admission constraints, HPA/VPA and node allocatable evidence for a captured selection. | Fixed bounded independent sources; explicit missing/stale inputs; no historical or aggregate scheduling claims. | Implemented for #59; integration pending |
| Storage diagnosis and expansion | Review bind/attach/mount/resize stages, topology and CSI evidence; explicitly preview supported PVC increases. | Bounded read-only diagnosis; exact UID/version/binding rechecks and guarded size-only requests; capacity is not usage and acceptance is not completion. | Implemented for #57; integration pending |
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
| #22 | Shared action discovery and help registry | Merged on master | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
| #23 | Retained fault-first snapshots and explicit log/stream modes | Merged on master | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
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

The documented 10,000-Pod filtering result has p95 **107.274 ms**, above the
100 ms target. The implementation is delivered; that performance target remains
unmet in the recorded sample. Live Relay and Gateway/HTTPRoute runs, other
operating systems and operator usability review remain explicit coverage gaps.
Named-provider desired sources, server previews, guarded recovery execution and
the deeper protocol/topology features listed above remain later increments.
