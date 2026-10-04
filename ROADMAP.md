<!-- Modified for k9+; see NOTICE. -->

# k9plus roadmap

Keep the basic resource browser fast and familiar. Advanced tools open on demand;
no background discovery on ordinary resource refreshes. Preserve existing shortcuts,
context safety, selection, and the return path from investigations.

## Areas and priorities

| Area | Useful improvement | How to keep it lightweight | Status |
| --- | --- | --- | --- |
| Navigation and actions | Search available actions for the selected resource instead of memorizing shortcuts. | One action menu; frequently used shortcuts still work. | Shipped on master (PR #6) |
| Resource troubleshooting | Bring conditions, recent events, restarts and ownership together. | Open an inspector on demand. | Shipped on master (PR #6) |
| TLS and certificates | Show expiry, SANs, issuer, chain information and where a certificate is used; optional endpoint checks. | A certificate view with deeper inspection rather than more columns everywhere. | Shipped on master (PR #6) |
| Workload relationships | Jump between Deployment, Pods, Service, EndpointSlices and Ingress/Gateway. | Contextual links before attempting a large topology map. | Open [PR #7](https://github.com/jaredpricedev/k9s/pull/7); UID/lifecycle gates in [#21](https://github.com/jaredpricedev/k9s/issues/21) |
| Change visibility | Compare explicit observations A/B with source, time and recreated identity. | A stays fixed; bookkeeping normalization is reversible. Desired-source comparison remains a later increment. | Open daily-workspace review stack below |
| Resource pressure | Investigate requests, limits, usage, OOM kills and scheduling evidence. | Workload-focused snapshots; missing usage is N/A and throttling stays unknown without counters. | Open daily-workspace review stack below |
| Context safety | Clear production identity, convenient read-only mode and destination-aware action confirmations. | Persistent compact indicators and full F2 destination details. | Open daily-workspace review stack below |
| Performance and interaction polish | Faster large lists, reliable cancellation, stable selection and consistent inspectors. | Improve existing workflows; retain bounded evidence and disclose measurements. | Open daily-workspace review stack below |

## PR #6 delivery

| Area | Approach | Delivery |
| --- | --- | --- |
| Navigation/actions | Audit shortcuts, add a searchable action menu, preserve existing navigation. | `:actions` on a resource list; reuse registered actions and their existing safeguards. |
| Resource troubleshooting | Conditions and events first, then restarts, ownership and resource jumps. | `:troubleshoot`: conditions, owners, scoped workload pods, container states/restarts and UID-scoped retained events; refresh and related-resource jumps. |
| TLS | Begin with certificate parsing, expiry, SANs and resource links. Review trust validation, active probes and sensitive-data handling separately. | `:tls` on Secrets/Certificates/Ingresses/Gateways; public metadata, references, offline trust verification and explicit verified endpoint probes from k9plus. |

PR #6 completes the scoped increments for these three areas; see [usage and limits](docs/resource-investigation.md).
It does not implement the five other roadmap areas. TLS revocation, mTLS client
identity, STARTTLS, in-pod probing and exhaustive cross-namespace consumer discovery
are outside this delivery. No certificate or network-policy changes are automatic.

Remaining Hubble features are tracked in [BACKLOG.md](BACKLOG.md). No merges or
policy application without user approval.

The daily-app correctness, visual foundation, investigation and evidence increments
are tracked in [issue #8](https://github.com/jaredpricedev/k9s/issues/8), with dependencies
and acceptance criteria in its linked issues. Proposed performance and usability goals
are targets until measured; fixture validation does not establish live-cluster coverage.

## Daily workspace review stack

The complete implementation is available on `codex/daily-kubernetes-trust`.
The focused PRs are open and unmerged; the branch includes the stack's prerequisites.
The existing workload-relationships PR is independently updated and reviewed.
The final validation PR records the review order, checks and captured evidence.

| Linked issue | Delivered behavior | Review |
| --- | --- | --- |
| #9 | Canonical selected resource identity, guarded synthetic views, nonzero fatal recovery | [PR #28](https://github.com/jaredpricedev/k9s/pull/28) |
| #10 | Validated drafts retain committed filters and resource selection | [PR #30](https://github.com/jaredpricedev/k9s/pull/30) |
| #11, #13 | Explicit metrics visibility and destination identity, correct memory trends | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
| #12 | Owned action-map snapshots and synchronized mutation | [PR #29](https://github.com/jaredpricedev/k9s/pull/29) |
| #14, #20 | Default-branch installation, first-run tasks, feature matrix, maintenance, CI and reproducible evidence | [Validation PR #39](https://github.com/jaredpricedev/k9s/pull/39) |
| #15 | Asynchronous restart, scale and delete with captured destination and UID conditions | [Operations PR #33](https://github.com/jaredpricedev/k9s/pull/33) |
| #16, #17 | Semantic/custom skins and readable compact fault layouts | [PR #31](https://github.com/jaredpricedev/k9s/pull/31) |
| #18, #19 | Retained frozen Hubble rows and bounded paginated workload scope | [Hubble PR #34](https://github.com/jaredpricedev/k9s/pull/34) |
| #21 | UID checks, canceled navigation, preserved source state | [Existing PR #7](https://github.com/jaredpricedev/k9s/pull/7) |
| #22, #23 | Shared action/help registry, retained fault-first snapshots and explicit stream modes | [Workspace PR #32](https://github.com/jaredpricedev/k9s/pull/32) |
| #24 | Explicit selected-only capability checks and verified Relay readiness | [Diagnostics PR #36](https://github.com/jaredpricedev/k9s/pull/36) |
| #25 | Immutable A/B observations with identity, noise controls and sensitive-content limits | [Comparison PR #35](https://github.com/jaredpricedev/k9s/pull/35) |
| #26 | Requests, limits, optional usage, last OOM and bounded scheduling evidence | [Pressure PR #37](https://github.com/jaredpricedev/k9s/pull/37) |
| #27 | Bounded reviewed JSON/Markdown evidence and offline import | [Evidence PR #38](https://github.com/jaredpricedev/k9s/pull/38) |

Review the core fixes #28–#30 independently, then the stack #31 → #32 → #33 →
#34 → #35 → #36 → #37 → #38 → #39. The stack already includes the core foundation
and its integrated refinements; the independent fixes provide a narrow delivery
option. Choose an integration path instead of assuming both sets can be merged
without reconciling their shared changes. Later stack PRs target their immediate
predecessor, so each diff shows one increment. PR #7 remains separate; it is not
part of the integrated daily-workspace branch. Rerun checks at the chosen revision.

See [dated validation](docs/validation-2026-10-04.md) for actual PTY, race and
disposable Kubernetes coverage. Live Relay/Gateway runs, other operating systems
and operator usability review remain explicit coverage gaps.
