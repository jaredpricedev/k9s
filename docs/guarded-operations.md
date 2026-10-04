# Guarded operations

Restart, scale and delete, node cordon/uncordon/drain, CronJob manual trigger and
suspend/resume, Helm rollback/uninstall, directory apply/delete, native kubectl
edit and plugins keep
session receipts in `:operations` (`:ops`). Press `r` to refresh, `n`/`p` to browse,
`c` to cancel remaining work, and Esc to return. Navigation keeps receipts and
the original destination. Closing the app cancels remaining waits.

Each receipt identifies its context, selection, start/finish time and per-target
outcome. Accepted stages remain visible even when a later step fails. ACCEPTED
means the server acknowledged the request; it does not mean a controller
finished. COMPLETED means an external command exited successfully; remote effects
were not independently observed. FAILED records an explicit rejection or failed
precondition. CANCELED means no write was attempted. UNKNOWN means a write may
have happened: inspect the captured destination before retrying. Cancellation
does not roll back cordons, evictions, Jobs or other accepted writes.

Native writes re-read the selected UID and use resource-version or UID write
preconditions. Drain retains kubectl ownership, DaemonSet/local-data filters,
PDB eviction handling and removal waits. Rejected PDB evictions can follow
kubectl's native retry policy; every actual eviction still carries the originally
reviewed Pod UID, so a same-name replacement is not evicted. Ambiguous writes are
never automatically retried. Conflict retries revalidate original identity.
Manual Jobs copy the native CronJob template and retain its owner UID; the
returned Job identity is recorded. Suspend/resume checks the reviewed scheduling
state and leaves existing Jobs running.

Helm freezes the committed browsing actor's transport before provider review and
checks provider fingerprints, pending-operation status and selected explicit
rollback revisions, then invokes Helm's own actions. Fingerprints are rechecked
before execution; they are not an atomic release lock. Helm Secret/ConfigMap
storage is supported. SQL storage fails closed because its storage API does not
expose operation cancellation. Helm receipts retain acknowledged HTTP paths and
hashes, not manifests, values, credentials or response bodies.

File apply/delete copies the committed browsing destination before confirmation and checks
the local file/directory fingerprint again immediately before starting kubectl.
It supplies a private temporary kubeconfig containing only that destination and
all resolved authentication, TLS and impersonation overrides. Native kubectl can
write many resources and is not atomic; this path supplies no individual resource
UID preconditions. Kustomize references remain resolved by kubectl. Use reviewed
rendered changes for per-resource evidence. Source traversal allows at most 500
regular files and 16 MiB; contents are not retained in receipts.

Plugins capture selected input/environment and arguments before dialogs. A
dangerous plugin always requires confirmation and cannot launch in read-only
mode. Native selected objects are checked before launch, but external plugin
writes do not acquire Kubernetes UID preconditions. Explicit plugin destinations
remain plugin-owned. Known kubectl/Helm plugins receive a captured default
kubeconfig resolved from the committed browsing actor before input/confirmation
dialogs. Arguments and pipeline stages run directly without shell expansion;
input values remain argv values. Background stdout is bounded and displayed only
when the plugin requests output display. It is not retained in receipts.

Commands and API waits have a bounded operation deadline (at most ten minutes).
Drain timeout `0` retains kubectl's native no-timeout option within that operation
deadline. Up to eight operations may run at once; up to 64 session receipts and
32 KiB of output per target are retained. Interactive tools use framework terminal
suspension; Ctrl+C interrupts the child. Unix process groups cover descendants
and restore terminal foreground ownership before resuming the UI. Windows uses
the native process-tree terminator with a bounded wait.

Regression verification uses fake Kubernetes clients, HTTP fixtures and local
child processes. It covers UID replacement, conditional writes, denied access,
PDB rejection/replacement, native ownership filters, partial accepted steps,
cancelled/unknown results, retained receipts, source changes, configuration
overrides, real background exit failures, bounded output and process descendants.
No live node drain, Helm release or Kubernetes mutation is required.
