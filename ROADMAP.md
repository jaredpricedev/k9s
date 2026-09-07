# k9plus roadmap

Keep the basic resource browser fast and familiar. Advanced tools open on demand;
no background discovery on ordinary resource refreshes. Preserve existing shortcuts,
context safety, selection, and the return path from investigations.

## Areas and priorities

| Area | Useful improvement | How to keep it lightweight | Status |
| --- | --- | --- | --- |
| Navigation and actions | Search available actions for the selected resource instead of memorizing shortcuts. | One action menu; frequently used shortcuts still work. | Active first increment |
| Resource troubleshooting | Bring conditions, recent events, restarts and ownership together. | Open an inspector on demand. | Active first increment |
| TLS and certificates | Show expiry, SANs, issuer, chain information and where a certificate is used; optional endpoint checks. | A certificate view with deeper inspection rather than more columns everywhere. | Active first increment |
| Workload relationships | Jump between Deployment, Pods, Service, EndpointSlices and Ingress/Gateway. | Contextual links before attempting a large topology map. | Backlog |
| Change visibility | Show what changed between resource observations and compare live configuration with an available desired source. | Explicit diff action with clear comparison sources. | Backlog |
| Resource pressure | Investigate requests, limits, usage, OOM kills and throttling. | Workload-focused view; show when required metrics are unavailable. | Backlog |
| Context safety | Clear production identity, convenient read-only mode and destination-aware action confirmations. | Persistent, compact context indicators. | Backlog |
| Performance and interaction polish | Faster large lists, reliable cancellation, stable selection and consistent inspectors. | Improve existing workflows without adding another tool. | Backlog; preserve baseline behavior in active work |

## Active first increments

| Area | Approach | Initial delivery |
| --- | --- | --- |
| Navigation/actions | Audit shortcuts, add a searchable action menu, preserve existing navigation. | `:actions` on a resource list; reuse registered actions and their existing safeguards. |
| Resource troubleshooting | Conditions and events first, then restarts, ownership and resource jumps. | `:troubleshoot`: an on-demand snapshot with conditions, owners, container states/restarts and UID-scoped recent events. |
| TLS | Begin with certificate parsing, expiry, SANs and resource links. Review trust validation, active probes and sensitive-data handling separately. | `:tls` on a Secret: parse only tls.crt; display bundle metadata and validity; do not claim endpoint identity or trust. Existing UsedBy action remains available through the action menu. |

These are bounded first increments, not completion of the entire areas. Later TLS
work includes cert-manager/Ingress/Gateway relationship navigation, explicit trust
stores, and opt-in endpoint probes with a clearly identified execution location.

Remaining Hubble features are tracked in [BACKLOG.md](BACKLOG.md). No merges or
policy application without user approval.
