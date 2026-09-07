# k9plus roadmap

Keep the basic resource browser fast and familiar. Advanced tools open on demand;
no background discovery on ordinary resource refreshes. Preserve existing shortcuts,
context safety, selection, and the return path from investigations.

## Areas and priorities

| Area | Useful improvement | How to keep it lightweight | Status |
| --- | --- | --- | --- |
| Navigation and actions | Search available actions for the selected resource instead of memorizing shortcuts. | One action menu; frequently used shortcuts still work. | Implemented in PR #6; pending review |
| Resource troubleshooting | Bring conditions, recent events, restarts and ownership together. | Open an inspector on demand. | Implemented in PR #6; pending review |
| TLS and certificates | Show expiry, SANs, issuer, chain information and where a certificate is used; optional endpoint checks. | A certificate view with deeper inspection rather than more columns everywhere. | Implemented in PR #6; pending review |
| Workload relationships | Jump between Deployment, Pods, Service, EndpointSlices and Ingress/Gateway. | Contextual links before attempting a large topology map. | Backlog |
| Change visibility | Show what changed between resource observations and compare live configuration with an available desired source. | Explicit diff action with clear comparison sources. | Backlog |
| Resource pressure | Investigate requests, limits, usage, OOM kills and throttling. | Workload-focused view; show when required metrics are unavailable. | Backlog |
| Context safety | Clear production identity, convenient read-only mode and destination-aware action confirmations. | Persistent, compact context indicators. | Backlog |
| Performance and interaction polish | Faster large lists, reliable cancellation, stable selection and consistent inspectors. | Improve existing workflows without adding another tool. | Backlog; preserve baseline behavior in active work |

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
