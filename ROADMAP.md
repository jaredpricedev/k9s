# k9plus roadmap

Keep the basic resource browser fast and familiar. Advanced tools open on demand;
no background discovery on ordinary resource refreshes. Preserve existing shortcuts,
context safety, selection, and the return path from investigations.

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
