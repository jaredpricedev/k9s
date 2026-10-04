# Owned local sessions

Open `:sessions` (or `:local-sessions`) to review port-forwards, container
shells/attach, node debug shells and plugins launched by this instance of k9+.
It does not discover or stop other processes. The overview works without an API
connection. `r` reads local state, `Enter` opens the selected lifecycle log,
`c` requests cleanup of that exact owned session and `Esc` returns to the
overview or originating page. Local cleanup remains available in read-only mode.

Each launch captures context, API server when asserted, resource, namespace, name, UID when available,
container and destination revision. A reconnect or later kubeconfig edit cannot
retarget its client. The selected preview shows state and local binding; details
retain full identity, start/end times and the operation receipt for external
commands or accepted writes. A stop request stays `STOPPING` until the worker
confirms local completion. `UNKNOWN` records an unconfirmed cleanup deadline or
unobserved remote command effects; it does not assert a rollback.

Launches still preparing belong to their originating page and are canceled if
that page closes or navigation changes before readiness. Established forwards
and background children survive navigation. App shutdown cancels only its owned
resources and waits up to five seconds for cleanup. Records are kept in memory:
at most 64 active local sessions, 128 ended records and 32 authored lifecycle
events per record. Operation workers have their separate concurrency limits and
a ten-minute maximum lifetime; port-forwards remain open until stopped.

## Port-forward endpoint access

Use the existing `Shift-F` action and forwarding dialog. Setup checks the captured
Pod UID, running phase, `get pods` and permitted `get` or `create
pods/portforward` access. It checks local bindings before opening a listener.
An occupied port or denied/absent endpoint leaves a failed record and never
stops another process. No automatic retry or debug container is created.

For a Service or workload, an eight-second worker verifies the captured origin
UID and resolves its selector in that namespace, with a 200-Pod lookup limit.
An absent selector never becomes an unrestricted Pod search. The record retains
both the originating resource and the selected running Pod identities; returning
to an existing session uses those captured identities without a fresh API read.

Setup and negotiation use an eight-second context. Captured TLS, credentials,
impersonation, transport wrappers and proxy settings are carried into websocket
or SPDY upgrades; cancellation closes an in-progress owned socket. Once the
stream is established, setup cancellation does not terminate it. A successful
record reports its actual local binding. Request deadlines do not establish a
separate process deadline for client-go external credential helpers.

Kubernetes' port-forward endpoint has no atomic UID precondition. k9+ checks the
UID before opening it; a same-name Pod replacement in the small interval after
that check remains an endpoint limitation. This is disclosed in the identity
boundary. Endpoint access remains available in read-only mode when RBAC permits
it, separately from debug-container writes.

## Shells, attach and node debug Pods

Container shell and attach use their existing actions. Before execution, a
worker checks the captured Pod UID, phase, container and required endpoint RBAC.
The native CLI receives a private single-context kubeconfig derived from the
captured connection. The file stays owned until the child and terminal handoff
finish, then is removed. Attach warns that Ctrl+C may interrupt the remote
process. Local child completion does not prove remote effects stopped. These
actions honor read-only mode.

A node debug shell explicitly confirms privileged Pod creation and host access
in the configured namespace. It verifies the selected Node UID and creation and
cleanup RBAC, then creates a unique Pod. Cleanup uses the returned Pod UID and
the captured client, including after cancellation. A same-name replacement is
retained, and k9+ never deletes an earlier or unrelated debug Pod. Create and
delete acceptance remain in `:operations`; cancellation does not erase them.
An unobserved create result or failed cleanup requires inspection of that
captured namespace/UID before retrying.

Known `kubectl debug` plugins are treated as writes even if their configured
`dangerous` flag is false. They require confirmation, read-write mode and the
appropriate create-Pod or patch-ephemeral-container access for a captured native
target. External plugin destination overrides remain owned by the plugin and
are disclosed; k9+ does not invent atomic UID preconditions for its CLI writes.

## Evidence boundary

The local lifecycle log contains authored status and cleanup facts. It excludes
command arguments, environment, raw process output and Secret content. An
operation receipt separately retains bounded command output where applicable.
Explicitly launched plugins own their actions and remote destinations; review
their configuration before use. No session is launched by opening the overview.

For reproducible fixture evidence, see `scripts/local-session-journeys.py` and
the captured-forward DAO regression tests. The PTY script launches disposable
local children and a local Kubernetes API; it does not use a live cluster.
