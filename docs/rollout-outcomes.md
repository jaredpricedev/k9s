<!-- Modified for k9+; see NOTICE. -->

# Rollout observations and outcomes

Select a native `apps/v1` Deployment, StatefulSet or DaemonSet and open
`:rollout` (or the controller rollout review action). Ordinary browsing does
not start monitoring or an external command. Reads retain the selected context,
namespace, GVR, name and UID, with observation timestamps and collection gaps.

| Controller | Status and revision evidence | Limits |
| --- | --- | --- |
| Deployment | Current generation, counts, pause/failure conditions; controlling-UID-owned ReplicaSets and their safe Pod templates | Template equality does not establish the controller-selected ReplicaSet |
| StatefulSet | RollingUpdate/OnDelete strategy, partition, reported current/update revision names; UID-owned ControllerRevisions and directly owned Pods | A partitioned completion applies only to the configured partition target. OnDelete requires operator Pod updates; absent counts or revision evidence remain unknown |
| DaemonSet | RollingUpdate/OnDelete strategy, desired/updated/current/ready/available and misscheduled counts; UID-owned ControllerRevisions and directly owned Pods | Node eligibility and unavailable/misscheduled counts affect convergence. Review does not update node scheduling policy |

A StatefulSet partition is an absolute ordinal. With `start: 5`, three replicas
and partition 6, Pods 6 and 7 are the update target while Pod 5 may retain its
prior template. Partitions below the first ordinal target the whole range;
partitions at or above the range end target no new Pods.

Custom rollout controllers and non-native API groups are unsupported. Missing
status fields remain unknown, including optional fields on older servers. A
controller has to observe the captured resource generation before older failure
conditions are treated as a current verdict. Controller availability does not
prove application, Service or traffic health. Child access remains independent;
denied revision history does not hide directly owned StatefulSet/DaemonSet Pods.

Pod detail distinguishes declared image references from runtime `imageID`
observations. The app does not resolve mutable tags. Evidence retains full image
IDs, normalized Pod template SHA256 fingerprints and resource versions. Template
checksum annotations and ConfigMap/Secret reference names are declared evidence;
they do not prove live or historical configuration resource versions. Secret
bodies and referenced configuration bodies are never fetched by this review.

The five tabs retain overview, revisions, Pods, recovery comparison and source
evidence. `j`/`k`, arrows and Page keys select exact revision UIDs; Enter explicitly
updates the recovery comparison. Refresh preserves a surviving selection and
invalidates a removed one until another revision is explicitly chosen. Recovery comparison remains read-only until a separate native Deployment
server-preview action is invoked. StatefulSet/DaemonSet recovery stays a retained
comparison; their controller revision data is not treated as an executable
rollback plan.

## Explicit bounded following

Press `w` to follow the captured controller generation and template. It performs
only named GETs in the captured namespace; it does not recollect children or run
an operation. The Overview shows elapsed time since this explicit observation
started, the target generation and read count. Elapsed time is not an inferred
rollout start time. Evidence retains the full follow-up reason and source.

Following stops when the controller reports a current-generation complete,
blocked, paused, manual-update or unknown verdict. A changed UID, generation,
template, kind or namespace stops continuity; the app never follows a replacement
resource or another release as the captured target. Denied/unavailable reads,
deadline, read budget and cancellation retain an unknown outcome. Default bounds
are two minutes, one-second intervals, 120 named reads and ten seconds per GET;
the underlying contract caps monitoring at five minutes. No automatic retry can
submit a write.

`w` stops following. Refresh, leaving the view or a destination change also ends
its ownership. Stopping observation does not cancel or roll back changes that a
different operation may have accepted. Original child evidence keeps its original
capture time; controller-only follow-up gets a separate timestamp and child
coverage statement. Retained outcomes stay separate from later refreshes.

The monitor contract supports an accepted-operation receipt containing operation
identity, acceptance time and the accepted controller generation/template. Such
a receipt changes the elapsed-time origin and clearly separates accepted writes
from observed controller completion. Read-only following is explicit. Native Deployment recovery also supplies the
actual operation receipt to the same observer after a persistent response is
acknowledged.

At 80×24 and 60 columns the view keeps one primary pane and exact identity in the
header/Evidence. The minimum task viewport is 40×12, excluding application chrome.
Below the minimum, a resize/back notice replaces clipped content. Keys and text
state labels remain meaningful without icons or color.

Validation covers bounded fake API reads, native tview SimulationScreen frames,
controller strategy/partition semantics, denied children, source continuity,
removed revision selection, timeout and cancellation after acceptance. These
checks establish request and rendering behavior; they do not constitute a live
production-controller or operator-usability trial.

## Guarded selected Deployment recovery

In Recovery, explicitly choose the retained revision with `j`/`k`, then Enter to
preview that exact UID. `x` opens a Cancel-first confirmation for server admission
preview. It reads the named captured Deployment and ReplicaSet and verifies both
UIDs, resource versions and template fingerprints, controlling ownership and the
captured Deployment generation. No namespace-wide inventory or Secret read is
made. Native StatefulSet/DaemonSet execution remains unsupported.

The server preview uses a bounded JSON patch with `dryRun=All`, strict field
validation and tests for target UID, resource version and generation. Its only
replacement is the entire selected Pod template, excluding the ReplicaSet's
`pod-template-hash` label. Deployment annotations and external configuration
contents are not historical recovery data and are not restored. Full safe
before/after values, source fingerprints and preconditions remain in Evidence;
raw template/patch bytes stay private. Admission acceptance applies to this
particular dry-run, and does not guarantee a future persistent request.

`a` opens a separate persistent-write confirmation with Cancel focused. If
commands or sensitive values were excluded from the safe comparison, a separate
checkbox must acknowledge those unreviewed fields. Visible managed-by, Helm,
Flux or Argo CD metadata adds a warning that reconciliation may restore the
previous template. These bounded markers are unverified metadata, not a composed
ownership claim, and remain in Evidence. Historical Secret references do not
restore Secret values. Read-only mode, changed
selection, abandoned/replaced forms or a changed viewing destination cannot
submit. After confirmation, the shared operation runner checks permission and
both sources again, then submits one exact pinned patch. Version/UID/generation,
ownership or source-template changes fail closed. There is no automatic retry.
A status-only resource-version update also requires refresh and a new preview.

`:operations` retains the accepted write and observed controller verdict after
navigation. Its receipt records target UID, accepted generation/template hash,
acceptance time, named observation count and final controller state. Monitoring
is bounded to two minutes/120 reads within a three-minute operation deadline.
No child evidence is relabeled as current. API acknowledgment and observed
controller completion remain distinct facts. Cancellation, timeout, denied
monitoring or superseding updates retain an unknown outcome; accepted writes are
not rolled back. An acknowledged response with identity/generation or template
differing from the preview is unknown and requires inspection before retrying.

Fake API and native-widget checks validate request scope, exact source/version
rejection, one-time submission, dry-run isolation, redaction, confirmation,
abandoned forms, read-only policy and separate accepted/controller receipts.
These are not a production-cluster recovery trial.
