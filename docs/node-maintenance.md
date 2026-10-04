<!-- Modified for k9+; see NOTICE. -->
# Node maintenance

Select a native Node and press `o`, or run `:maintenance`, to review its affected
workloads before changing it. The Node `r` drain action opens this preview too.
Drain planning reviews one Node at a time; marked cordon/uncordon remains
available in the Node list. The preview is available in read-only mode.

`1–6` or Tab selects Overview, Workloads, PDBs, Constraints, Outcomes and
Evidence. `r` refreshes independent observations; `/` searches the active tab,
and Esc returns to the previous view without losing the list selection. Failed
refreshes preserve the previous snapshot and show that it is retained.

The overview shows observed affected Pods and potential blockers. Workloads
retains original Pod UIDs, controller references, readiness/phase, grace periods,
EmptyDir, hostPath and PVC references. Controller references do not verify
controller existence or replacement health. Constraint evidence includes node
selectors, affinity, tolerations, topology spread, scheduler and priority class.
Aggregate free capacity does not prove replacement Pods can schedule.

PDB evidence includes selectors, observed generation, reported allowance and
healthy counts. A nil policy/v1 selector matches no Pods; an empty selector
matches all Pods in its namespace. Stale status, invalid selectors, missing
readiness, denied reads and multiple matching PDBs remain explicit uncertainty.
AlwaysAllow and IfHealthyBudget can relax allowance for some unready Running
Pods; zero allowance is a potential blocker, and a positive allowance is not an
admission permit. Guarded submission checks actual eviction/delete permission;
the API decides admission.

Press `d` to review drain options, `c` to confirm cordon, or `u` to confirm
uncordon. Ignore DaemonSets skips their Pods; it does not delete them. Deleting
EmptyDir data and forcing unmanaged Pods require explicit choices. Disable
Eviction performs direct deletion and bypasses PDB admission.
Native kubectl also falls back to direct deletion when the eviction API is
observed unsupported; denied/interrupted discovery is an error, not unsupported.
Grace `-1` retains the Pod default, and `0` requests immediate termination. The new
preview starts with a 5-minute wait so ordinary Pod grace periods can finish. Timeout `0` retains
kubectl's native sentinel; the application wait is still bounded to 10 minutes.

Drain requires a complete bounded Pod page with valid, unique identities. A new
or recreated Pod detected before submission requires a refreshed preview. Native
kubectl filters, PDB admission, retries and deletion waits remain in use. The
captured Node UID is rechecked before cordon and before Pod writes; every actual
Pod eviction/delete carries its original Pod UID precondition. Kubernetes does
not provide an atomic transaction across Node and Pod operations.

`5 Outcomes` records accepted cordon and evictions separately from native
observations of original Pod removal. `x` cancels remaining work while it is
running. Cancellation never uncordons the Node or rolls back accepted writes.
Denied, partial and unknown outcomes retain their receipt in `:operations` after
navigation. UNKNOWN requires inspecting the captured destination before retrying;
there is no automatic mutation retry after an unknown result.
Every subsequent maintenance action requires a refreshed observation captured
after the preceding operation's outcome.

Refresh after maintenance for independent Node schedulability/readiness and the
count of original reviewed Pod UIDs still observed on that Node. Absence from a
complete node page is not proof of deletion, healthy replacements or application
recovery. Uncordon remains an explicit reviewed action.

Collection uses a named Node GET, one node-scoped Pod page (500), and PDB pages
in up to 32 observed Pod namespaces with a total limit of 500 budgets. Each read
has a 3-second deadline inside a 10-second collection. Source, time, limits,
denied/partial coverage and full captured identity appear in Evidence. No Secret
API reads, environments, container commands, arbitrary annotations, hostPath
paths or volume credentials are retained.

The single-pane view preserves identity, status, active tab and actions at
80×24, 60 columns and the supported 40×16 terminal floor (40×12 task viewport).
Smaller viewports show the size notice. State meaning uses text and works without
icons or color. The shared responsive form host owns the drain options layout.

See [the fixture validation](node-maintenance-validation-2026-10-04.md) for the
checked behaviors and boundaries.
