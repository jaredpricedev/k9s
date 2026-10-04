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
invalidates a removed one until another revision is explicitly chosen. Recovery
comparison remains read-only; no rollback or server dry-run is submitted.

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
from observed controller completion. This increment exposes read-only following;
guarded recovery execution is a subsequent operation-runner integration.

At 80×24 and 60 columns the view keeps one primary pane and exact identity in the
header/Evidence. The minimum task viewport is 40×12, excluding application chrome.
Below the minimum, a resize/back notice replaces clipped content. Keys and text
state labels remain meaningful without icons or color.

Validation covers bounded fake API reads, native tview SimulationScreen frames,
controller strategy/partition semantics, denied children, source continuity,
removed revision selection, timeout and cancellation after acceptance. These
checks establish request and rendering behavior; they do not constitute a live
production-controller or operator-usability trial.
