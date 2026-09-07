# On-demand resource investigation

PR #6 adds action discovery, resource troubleshooting and TLS investigation while
keeping the ordinary resource browser unchanged. All API reads are on demand.

## Actions

`:actions` opens a searchable menu for the selected resource. Type words in any
order, use arrows/Enter to run, Backspace to revise and Esc to return. Categories
identify built-in actions, plugins and configured shortcuts; dangerous actions
are labeled when their registrations identify them. Existing handlers retain
confirmation and read-only restrictions. The selected context/path are checked
again before invocation. No match is shown explicitly; it does not run an action.
The menu includes Troubleshoot and TLS where supported, plus existing log and
owner/reference navigation. No resource-list shortcuts were reassigned.

## Troubleshooting

`:troubleshoot` shows a read-only snapshot of the selected object's owners,
conditions and container states/restarts. Deployment, DaemonSet, StatefulSet,
ReplicaSet and Job selectors also load up to 100 matching pods with current and
previous failure states. Empty selectors never expand to the namespace.
Selector matches are labeled as such; they are not asserted to be owned pods.

Up to 100 retained events are requested by the selected object's UID, sorted by
available event timestamps. Missing conditions/events do not establish health.
RBAC errors and truncated results are explicit. Child pod events are available by
jumping to the pod and inspecting it; the parent view does not perform an unbounded
per-pod event scan.

- **r** refreshes the snapshot (not an automatic watch).
- **g** lists owners, matching workload pods and reported references. Enter jumps
  to a resource; Esc returns to the preserved snapshot.
- Use existing actions for logs, describe and owner navigation after a jump.

Reads have a ten-second deadline and cancel on exit. Each inspector pins its
originating client transports; switching context cannot redirect a pending read
to the same resource name in another cluster. Stale callbacks never update another
screen. Failed API reads show explicit errors rather than a healthy empty result.

## TLS metadata and relationships

`:tls` supports Secrets, cert-manager Certificates, networking.k8s.io Ingresses
and Gateway API Gateways referencing TLS Secrets. Public certificate bundle
members show subject, issuer, validity dates, SANs and SHA256 fingerprints.
The inspector never displays or parses a private key.

**g** jumps to Secret/issuer/owner references. From a Secret it additionally scans
same-namespace Pods, Ingresses, Certificates and Gateways for configuration
references, bounded at 200 objects per type. Missing APIs/RBAC and truncation are
shown. Pod secret volumes, projected secret sources and container env references
are recognized. Cross-namespace consumers and controller-specific annotations are
not scanned. A configured/mounted Secret does not prove the endpoint serves its
current contents.

Cross-namespace Gateway Secret references are displayed but not automatically
fetched. Use controller conditions and ReferenceGrants to investigate acceptance;
this view does not claim a reference is authorized. Existing cert-manager
Issuance/References actions provide the issuance-chain navigation.

## Offline certificate trust verification

From a Secret's TLS inspector press **v** and provide the intended server name.
Choose system roots (default) or an explicit absolute local CA file. Alternatively,
from a selected Secret resource row:

```text
:tlsverify service.example.test
:tlsverify service.example.test ca=/absolute/path/ca.pem
```

The first certificate is treated as the server leaf; remaining bundle members are
candidate intermediates, never implicit trust anchors. Verification checks chain,
validity, server-auth usage and the requested hostname against the named trust
source. It does not inspect the private key or contact an endpoint. The k9plus
machine's clock and system roots are used, not a pod's trust store. Revocation is
not checked. CA files must be regular files, contain only CA certificate PEM
blocks, and fit within 1 MiB. Fields/paths with whitespace are not supported.

## Explicit TLS endpoint probe

Press **p** in a TLS inspector. The form states the execution location and requires
an explicit **Run TLS handshake** action. Opening/cancelling the form does no IO.
The equivalent command is:

```text
:tlsprobe example.com:443 example.com
:tlsprobe 127.0.0.1:9443 service.example.test ca=/absolute/path/ca.pem
```

The first argument is the TCP destination; the second is the identity to verify
(and SNI name for DNS names). This connects **from the machine running k9plus**, not
from a pod. It uses verified TLS, minimum version 1.2, with system roots or the
explicit CA file. There is no insecure mode or automatic plaintext downgrade.
No HTTP request is sent. Success reports peer address, protocol, cipher and public
certificate metadata; failure preserves the verification error and trust source.
**r** on a probe result explicitly repeats the handshake. No client certificate,
STARTTLS, revocation check, in-pod probe or automatic remediation is provided.

## Verification and operation

Focused tests cover generated PKI, wrong names, unknown roots, expired certificates,
bundle-root non-trust, cancellation and a real local TLS server that asserts zero
HTTP requests. Fake clients cover selector scope, empty selectors, permission
errors, truncation, context-pinned transports and cross-namespace non-fetching.
View tests cover literal markup and readable redraws. Lab evidence is recorded
with the PR; broader version/scale matrices remain unverified.

In the iximiuz browser terminal, launch directly on **dev-machine**:

```sh
/home/laborant/k9plus-investigate -n hubble-test
```

The tmux/browser combination showed stale characters when opening the command
prompt; direct launch resolved this in user testing. Existing sessions and the
logging lab are preserved.

API references:
- [Go X.509 parsing and verification](https://pkg.go.dev/crypto/x509)
- [Go TLS dialer](https://pkg.go.dev/crypto/tls#Dialer)
- [Kubernetes TLS Secrets](https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets)
- [Gateway API TLS references](https://gateway-api.sigs.k8s.io/guides/user-guides/tls/)

See [ROADMAP.md](../ROADMAP.md) for the eight areas and remaining Hubble backlog.
