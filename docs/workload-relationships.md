<!-- Modified for k9+; see NOTICE. -->
# Workload relationships

From a resource list, run `:troubleshoot` (or `:ts`), then press `g` for related resources.
Choose a row and press Enter to jump. Esc returns to the captured inspector;
`r` refreshes the inspection. Reopening `g` takes a new relationship snapshot.
The ordinary lists gain no additional polling or shortcuts.

Known ownerReference and EndpointSlice targetRef UIDs are retained separately
from name-only configuration references. Selector-discovered objects also retain
their observed UID. Picker rows state the UID or explicitly mark it unknown.
References with different UIDs stay separate when evidence is deduplicated.
The source snapshot's observed UID is checked when collecting relationships.
Enter checks the target on the original pinned connection before navigating;
same-name replacements stay in the picker with an identity-change message.
The destination resource list carries that expected UID into subsequent
inspection. Closing the picker cancels a pending jump, and responses after a
context change or deadline cannot navigate another view.

For the row opened from a relationship, live YAML, Describe and kubectl Edit
require reopening its native resource list to select the current object. Those
by-name paths cannot retain the relationship's UID guarantee. Use Inspector for
UID-checked evidence, or explicitly reopen the list for the usual native actions.
Other rows in the list keep their ordinary actions.

Nested relationship jumps retain each source snapshot. Back restores the earlier
viewing namespace only while that jump still owns its destination. Manual context
or namespace changes, including reactive disk edits and changes away and back,
end that ownership. A valid nested return preserves the outer jump's ownership
without reviving a ticket invalidated by a user change.

| Starting resource | Related resources and evidence |
| --- | --- |
| Deployment, StatefulSet, DaemonSet, ReplicaSet, Job | Existing selector-matching pods and owners; Services whose selectors match pod-template labels. Template matches are configuration evidence, not current endpoints. |
| Pod | Owners; Services whose complete nonempty selectors match the pod labels. |
| Service | Selector-matching pods; EndpointSlices with its service-name label; same-namespace Ingress and HTTPRoute backend references. |
| EndpointSlice | Service-name label; explicit Pod targetRefs; endpoint addresses and reported ready/serving/terminating conditions. Addresses without supported Pod references remain informational rows. |
| Ingress | Default and rule Service backends, plus existing TLS Secret links. |
| HTTPRoute | Gateway parentRefs and Service backendRefs, preserving explicit destination namespaces. |
| Gateway | Same-namespace HTTPRoute parentRefs, plus existing TLS Secret links. |

Each network relationship states why it appears. A selector match does not prove
endpoint readiness. A backend reference does not prove successful traffic. A
parentRef does not prove Gateway attachment. Inspect the related resource's
reported conditions or use Hubble to investigate observed traffic.

Discovery is read-only, cancellable, bound to the originating context and limited
to 200 objects per resource list. Existing workload pod lookup remains capped at
100. Permission failures, missing APIs and truncated responses are shown. Lists
are sorted and remain unchanged while inspecting the picker. Multi-resource
reads are not an atomic cluster snapshot.

Reverse discovery scans only the current namespace. Cross-namespace forward
references are labelled but not automatically fetched: Enter is an explicit jump
using current RBAC. ReferenceGrant, allowedRoutes, attachment status, backend
weights and ports are not evaluated. Only HTTPRoute v1 is included in this
increment; other route types and exhaustive cross-namespace graphs remain future
work. No policies, certificates, routing rules or L7 settings are changed.

API semantics follow the official [EndpointSlice documentation](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/),
[HTTPRoute documentation](https://gateway-api.sigs.k8s.io/reference/api-types/httproute/)
and [cross-namespace reference rules](https://gateway-api.sigs.k8s.io/reference/api-types/referencegrant/).

Current validation uses view fixtures for source/target UID replacement, unknown
UIDs, ownership and endpoint identity, deduplication provenance, cancellation,
deadline, permission denial and API-group collisions through the complete loader.
Inspection fixtures also cover retained query/scroll/snapshot navigation and
problem-first CrashLoop/OOM/event evidence. No live cluster was used to revalidate
this identity and lifecycle review.

Historical validation from the original PR: focused view tests exercise exact/partial/empty Service selectors,
namespace boundaries, default/rule Ingress backends, explicit EndpointSlice
Pod targets, cross-namespace HTTPRoute references, permission/truncation notices,
and Istio/Gateway API kind collisions. The iximiuz Hubble lab verified Service →
EndpointSlice → Pod jumps, Pod → Service lookup, and Ingress → Service lookup.
HTTPRoute CRDs are absent in that lab: missing-API visibility is smoke-tested;
Gateway/HTTPRoute references are fixture-tested. Browser rendering and large
cluster performance were not revalidated in this increment.

The combined integration passes the complete ordinary Go suite, all eight race
suites, and full lint with zero issues. Additional regressions cover failed
context switches, reactive disk destination changes, nested and repeated
cross-namespace returns, changes away and back, expired pickers, native action
UID guards, and retained inspection search/scroll state through skin changes and
Back. Raw output and source provenance are recorded in
[the integration evidence](evidence/merge-2026-10-04/README.md). VCS stamping was
disabled for managed worktree checks because automatic Git status collection
cannot read the worktree metadata in the sandbox.
