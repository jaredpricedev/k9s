<!-- Modified for k9+; see NOTICE. -->
# Storage diagnosis and reviewed expansion

Select a native Pod, PersistentVolumeClaim, PersistentVolume or StorageClass,
then run `:storage` or choose **Storage diagnosis and expansion preview** from
Ctrl-O. The opening selection must have a captured UID. Diagnosis performs reads
and works in read-only mode. `1`–`5` select Overview, Claims, Topology, CSI and
Evidence; Tab/Shift-Tab cycle; `r` explicitly refreshes; `/` searches; Esc returns.
Tabs retain search, scroll and selection through resize and skin changes.

The overview separates binding, attachment, mounting and resizing evidence.
Bound PVCs, attached VolumeAttachments and provisioned capacity do not establish
a mounted filesystem, actual usage or free space. Usage stays **not configured**
until a named provider reports usable measurement time and coverage. Pending
claims using WaitForFirstConsumer may be waiting normally for scheduling.
FilesystemResizePending and allocated resize statuses remain controller/node
progress evidence; online expansion may be supported, so no automatic restart is
recommended.

Claims show requested storage, status capacity, conditions, allocated resource
statuses, selected-node annotations and Pod consumer references. Topology shows
PV node-affinity terms, class binding mode, provisioner, expansion permission and
allowed topology. These configuration terms do not prove scheduler compatibility:
this review does not collect all node labels or scheduler policy. CSI evidence
includes readable CSIDrivers, CSINodes and VolumeAttachments. An absent attachment
is expected when the observed driver explicitly has attachRequired=false. Missing
or denied driver evidence remains unknown. CSIDriver metadata does not establish
ControllerExpandVolume or NodeExpandVolume runtime capabilities.

The selected object is independently GET-checked against its captured UID before
eight fixed, independently bounded sources: Pods, PVCs, PVs, StorageClasses,
CSIDrivers, CSINodes, VolumeAttachments and core Events. Each source has a
three-second deadline and a 100-object page bound; the overall review wait is ten
seconds. Continue tokens or oversized pages remain partial without pagination.
Source states retain read/attempt time, visible count and bound independently.
Denied CSI does not suppress readable claims, topology or events. A failed refresh
retains the original observation and time with its failure shown. Destination
changes or a recreated selected object require reopening.

Pod/PVC consumers are scoped to the opening namespace, except a selected PV with
a reported claim namespace uses that namespace. An explicit all-namespaces scope
remains bounded. PVC-to-PV claims are checked by namespace/name/UID when reported.
Pod volume references and VolumeAttachment associations contain names, not PVC or
PV UIDs. Retained events require matching observed object UIDs and may involve
another volume on the same Pod. Independent reads are not atomic or a complete
history. No Secret API is queried; CSI attributes, volume handles, CSI node IDs,
Secret references, mount options and StorageClass parameters are excluded from
the retained diagnosis.

Press `e` to select one retained PVC and enter a new requested size, for example
`20Gi`. Preview performs no writes. It requires a Bound, non-deleting PVC with UID
and resourceVersion, a verified PV claim UID, a reported nonempty StorageClass
with UID/version and provisioner, and explicit allowVolumeExpansion=true. No
default class is guessed from a missing/empty claim class. The new quantity must
strictly exceed the current spec request; shrinking, equal requests and failed
expansion recovery that lowers the failed request are outside this workflow.

The second form shows context, exact PVC/StorageClass/PV identities, reviewed
versions, old/new request and observed status capacity. Read-only mode permits
preview and blocks submission. Explicit **Submit request** enters the guarded
operation registry. The worker uses the captured connection, checks PVC GET/PATCH
authorization, freshly reads the PVC/PV/StorageClass, rejects changed PVC/class
UID or version and changed PV UID/claim binding, then applies only the requested
storage field with atomic JSON Patch tests for PVC UID and resourceVersion. The
bounded operation wait is ten seconds. It does not silently replan or retry a
conflict or ambiguous network outcome. Related-resource reads cannot eliminate
cross-resource races; the Kubernetes API still decides whether to accept it.

`:operations`/`:ops` retains per-target acceptance, failure, cancellation or
unknown receipts after navigation. API **ACCEPTED** requests are distinct from
controller and filesystem completion. Refresh storage evidence to inspect new
capacity, conditions and allocated resize statuses; leaving the screen or
canceling remaining operation work does not undo an accepted request.

See [native validation and reproduction](storage-review-validation-2026-10-04.md).
