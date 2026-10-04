# Background resource operations

Restart, scaling and deletion capture the selected cluster, namespace, name and
Kubernetes UID before confirmation. Confirmations show that destination. A
context, namespace, screen or read-only change requires a new confirmation. Each
submitted request uses the captured transport, so switching contexts cannot
redirect it. UID checks reject an object recreated with the same name.
Resource-version preconditions reject changes between validation and the write;
an explicit version conflict is retried within the deadline after rereading and
verifying the original UID. Timeout and transport errors are never retried.

Network requests run outside the input and draw loop. Scaling reads the desired
replica count asynchronously; Cancel or Esc stops that waiting read. Counts must
be between zero and 2147483647. When the current count cannot be read, the form
requires an explicit intended value.

A confirmation submits the intended write. **Accepted** means the API accepted
that request; it does not mean a rollout finished, all replicas became ready, or
finalizers completed deletion. Watch READY, STATUS and Events for the resulting
controller state. Leaving a screen does not undo a submitted request. A deadline
ends the wait; acceptance may be unknown, so inspect the resource before retrying.

Marked selections receive independent outcomes. An unavailable identity is not
submitted, and one failed resource does not stop subsequent resources. Successful
deletions clear their marks; failed marks remain. Batch results show every
selected destination, accepted requests and failures in a persistent result view.
The batch has a single configured API deadline and accepts at most 100 selected
resources. Targets not submitted before that deadline are reported independently.

Kubernetes authorizes requests on the captured connection. Background
SelfSubjectAccessReview checks also use its captured typed client. API calls are
bounded by the configured call timeout. Synthetic file, Helm and port-forward
deletions also run in a worker; Helm uses captured context flags and binds its API
and hook transports to the deadline. Synthetic records use their native
identities, which do not provide Kubernetes object UID preconditions.

## Disposable Kubernetes verification

`TestDisposableKubernetesOperationsAndPressure` runs only when
`K9PLUS_AUDIT_KUBECONFIG` names an isolated kubeconfig whose current context is
`kind-k9plus-audit` and whose API endpoint is loopback. Ordinary tests skip it and
never load a default kubeconfig. It creates and removes its own namespace,
verifies real restart/scale/delete acceptance, resource-version and UID
preconditions, same-name replacement rejection, and pressure/scheduling evidence
without metrics-server. It does not establish controller completion or validate
Hubble Relay, Gateway APIs, TLS integrations or production connectivity.

```sh
kind create cluster --name k9plus-audit --image kindest/node:v1.34.0 \
  --kubeconfig /tmp/k9plus-audit-kubeconfig
K9PLUS_AUDIT_KUBECONFIG=/tmp/k9plus-audit-kubeconfig \
  go test ./internal/view -run TestDisposableKubernetesOperationsAndPressure -v
kind delete cluster --name k9plus-audit --kubeconfig /tmp/k9plus-audit-kubeconfig
rm /tmp/k9plus-audit-kubeconfig
```
