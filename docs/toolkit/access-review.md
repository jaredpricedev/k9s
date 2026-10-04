# Concrete access decisions

Use `:access` to ask one resource authorization question. The native keyboard
form accepts `self` or an explicit username, exact optional groups, verb, full
GVR, explicit namespace or cluster scope, optional subresource and optional name.
A selected resource supplies defaults and its captured UID; editing its identity
clears that UID. Authorization APIs evaluate names and attributes, not resource
UIDs. Resource scope is an explicit input; this workflow does not discover APIs.

Press **Review** to submit one nonpersisted `SelfSubjectAccessReview` for self or
`SubjectAccessReview` for the supplied named subject and groups. Self also requests
one `SelfSubjectReview` for the current authenticated identity to explain visible
RBAC candidates. This resolves only the caller; it never enumerates identities or
probes broad permission sets. Empty explicit groups exclude group grants and may
differ from that user's authenticated memberships. No membership is inferred.

The result keeps permission to submit the review separate from the access
decision. A forbidden review request means the decision is unavailable, rather
than that the subject's questioned action is denied. Review reasons, evaluation
errors, source and time remain visible. Unsupported, canceled and failed requests
also remain unavailable. Authorizer evaluation errors are shown independently.

After a successful review request, the workflow reads one bounded page of up to
100 ClusterRoleBindings and, for a namespaced question, 100 RoleBindings in that
namespace. It reads at most 32 referenced Roles/ClusterRoles for subject matches,
and considers at most 100 rules per referenced role. Every retained candidate
keeps binding/role name, scope and UID. Truncated pages and inaccessible bindings
or roles make explanation coverage incomplete. Cluster questions never use
namespaced RoleBindings. Missing visible matches do not establish absent grants.

These are candidate rule matches, not proof of why an authorizer decided. Other
authorizers, inaccessible policy and races are not explained. Requests are not an
atomic policy snapshot. Quota/admission and later identity changes can affect real
operations even when authorization is allowed. Use the native `:roles` or
`:clusterroles` browsers to inspect referenced rules independently.

Parent-resource `create` and `deletecollection` must clear the optional resource name because
real requests authorize the collection. Creating a named subresource such as
`pods/exec` can retain the parent name. Named `list`/`watch` questions represent
name-constrained authorization; a real request needs the corresponding
`metadata.name` field selector. Subresources are independent resources for RBAC
matching (`pods/log` does not grant `pods`). Non-resource URL questions are outside
this workflow.

`e` edits the retained question; `r` explicitly reviews it again. `Tab` moves
through fields/buttons and `PageUp/PageDown` scrolls form instructions. `Escape`
cancels forms; `Escape`/`q` returns to the retained owner and cancels pending work.
Destination revision, captured context and generation checks discard abandoned
or late callbacks, including namespace/context round trips. Forms work at 80×24,
60×18 and 40×16; the retained text view supports a 40×12 viewport. Meaning is
conveyed by text in monochrome and without icons.

All collection shares an eight-second deadline. Only review objects and RBAC
reads are requested. The questioned resource, including Secret bodies, is never
read, created, patched or deleted. No credentials or installed agents change.
Tests use API fixtures and terminal simulation; they do not claim live-cluster
or operator-study validation.
