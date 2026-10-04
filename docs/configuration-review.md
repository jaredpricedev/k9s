# Declared configuration review

Select a native Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet,
ReplicationController, Job, CronJob, ConfigMap or Secret and run
`:config-review`. Ctrl-O also exposes **Declared configuration references**.
The selected source must have a captured UID and an explicit namespace.

The review has five tabs. **Overview** summarizes retained declarations and
collection gaps. **References** shows environment variable names, `envFrom`
prefixes, optionality, projected item paths, mounts and `subPath` usage.
**Consumers** shows visible same-namespace Pod and controller declarations.
**Changes** compares the two most recent successful metadata/key-name captures.
**Evidence** retains exact source identities, resource versions, capture times
and coverage. Use Tab/Shift-Tab or 1–5, `/` to search, `r` to explicitly refresh,
and Esc to return. Search and scroll position are retained separately per tab.

## Interpretation and privacy

A declared reference does not show the environment of a running process or the
bytes currently mounted in its filesystem. Environment references can suggest
a rollout check; they do not prove a rollout is necessary. Kubernetes `subPath`
mounts do not receive projected-volume updates, and other projected updates are
asynchronous. Runtime contents and independent writers were not observed. Use
`:rollout` for available controller/revision evidence.

ConfigMap `data` and `binaryData` are reduced to key names. Their values, literal
environment values and annotation values are not retained or displayed. Secret
GETs request **only** `meta.k8s.io/v1 PartialObjectMetadata`. The reader has no
ordinary JSON/full-object fallback, rejects incompatible responses and never
lists Secrets. Servers that cannot negotiate metadata produce an explicit gap.
Secret key inventory stays unknown; a key named in a workload selector is a
declaration, not evidence that the key exists in the Secret.

Missing objects, required missing ConfigMap keys, optional missing references,
denied reads and unknown key visibility remain distinct. ResourceVersion changes
are metadata-version observations, not proof of value changes. Missing or denied
captures do not establish deletion. Pod and controller declarations can overlap;
the consumer list is not a replica count or a global impact estimate.

## Scope and limits

Collection is read-only and explicit. It verifies the selected source UID,
retains at most 64 declarations and 256 ConfigMap key names, and reads a single
page of at most 100 consumers for each of eight native kinds. A continuation or
larger page is incomplete coverage. Consumer discovery stays in the selected
namespace and only follows the source's initially retained configuration names;
it does not expand into an unrelated dependency graph.

Collection has a 10-second context deadline. Strict metadata HTTP requests also
have a 3-second client timeout and a 512 KiB response bound. External credential
helpers configured through client-go are not separately process-supervised by
this reader. A failed refresh retains the previous capture and its original
time. Navigation cancels pending work; results from a changed view, context or
destination revision are discarded.

Native frame tests cover 120×34, 80×24, 60×18 and 40×12, plus recovery from a
smaller terminal. Domain and real HTTP tests cover strict metadata negotiation,
full-object rejection, response sanitization, cancellation, mixed references,
missing/optional/denied sources, consumer request limits and UID replacement.
These are automated fixtures, not a live production-cluster validation or an
operator usability study.
