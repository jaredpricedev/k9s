# GitOps ownership and reconciliation review

Select an Argo Application, Flux reconciler/source, or Kubernetes resource and
run `:gitops`. The view reads the captured context, resource path and UID. It
does not change the cluster or run an external provider. Read-only mode supports
the whole review.

The four tabs put reported reconciliation state first, followed by the explicit
reference chain, separate version evidence, and retained evidence with source
times and collection gaps. Use `1`–`4` or Tab/Shift-Tab, `/` to search the current
tab, `r` for an explicit refresh, and Esc to return. Search and scroll stay with
each tab. A failed refresh retains the previous observation; a changed context
or workspace namespace requires reopening the review. The view supports a
40×12 content rectangle (40×16 with application chrome) and explains smaller
terminal sizes without discarding evidence.

Controlling owner references are checked against the named target's UID. Flux
tracking needs its namespace/name label pair. Argo self-resource tracking IDs or
tracking labels need an explicit Application namespace when the resource does
not provide it: `:gitops control`. The view does not assume `argocd`, scan every
namespace, or treat a general instance label as proof. Readable tracking targets
remain **unverified metadata**; Argo inventory lacks resource UIDs. Helm release
metadata is shown with that limitation; Helm Secret release storage is not read.
Malformed markers, replacement UIDs, reference cycles, permission refusals,
missing named resources and absent controller APIs remain visible as gaps.
Controlling owners are prioritized before the eight-reference per-object limit;
extra controlling/dependency references and unresolved dependency selectors
produce explicit partial coverage rather than a complete chain.

Argo sync, health, automation, operation state, conditions and reconcile time are
controller reports. A previous Healthy/Synced report cannot hide a failed current
operation or blocking condition. Argo has no general status observedGeneration
guarantee, so the review never labels that report as current specification
convergence or workload readiness. ApplicationSet identities and conditions can
appear through controlling owner references; generated applications are not
listed or inferred.

Flux reuses the native supported-resource model, including suspension and opaque
reconcile-request handling. Reported generation mismatches are outdated evidence;
DependencyNotReady remains a dependency wait. OCI HelmRepository resources have
no reconciliation status. Kustomization applied revision and source artifact
revision can be compared only as independent reported source observations.
Their difference is a progress gap, not proof of failure.

Chart version/constraint, Helm application version, Git/source revision, artifact
digest and reported image references have separate provenance. Argo multi-source
entries retain their declared/reported positions without inventing a pairing.
Reported image references do not establish a running Pod image digest. Free-form
controller diagnostic messages, raw manifests, Helm values, repository URL
credentials/query parameters and all Secret bodies are excluded from retained
projections.

Collection is bounded to 16 named identities, depth six, 15 seconds overall and
three seconds per native read. Native API discovery checks only the referenced
group/version, with 32 metadata requests per collection. Inventories, conditions,
source fields and displayed evidence are capped. Independent reads are not an
atomic snapshot. No reconcile, suspend, sync or repository-write action is
offered by this first read-only adapter; guarded action adapters need their own
reviewed plans and outcome tracking.
