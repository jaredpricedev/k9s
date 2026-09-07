# On-demand resource investigation

These first increments keep the resource browser unchanged. From a resource list:

- `:actions` opens a searchable list of visible registered actions. Type to filter,
  use arrows and Enter to run, Esc to return. Existing action handlers retain their
  normal confirmation behavior. Troubleshooting and Secret TLS are also listed.
- `:troubleshoot` loads a read-only snapshot of the selected object, owner references,
  conditions and container states/restarts. It then requests up to 100 events using
  the object UID. API errors and truncation are shown explicitly. It does not
  aggregate child pods for a Deployment or infer health from missing conditions.
- `:tls` on a Secret parses certificates from `tls.crt`. Every bundle member shows
  subject, issuer, validity dates, SANs and SHA256 fingerprint. It neither displays
  nor checks the private key. Use the existing Secret UsedBy action for references.

Inspector reads happen off the UI thread with a ten-second deadline, cancel on
exit, and never update another screen. They are snapshots: reopen to refresh.
No requests are added to the normal browser refresh loop, and no new shortcuts
are reserved on resource lists.

Certificate validity is evaluated against the k9plus machine's clock. Parsing
and validity do not establish a trusted chain, hostname match, key match, revocation
status, or which certificate a server actually presents. There are no active probes.
Secret GET permission is required; Kubernetes returns the complete Secret object,
but this view projects only public certificate metadata and never logs Secret data.
The inspector does not offer a raw Secret export; generic text-view copying/saving
contains only the rendered snapshot.

API references:
- [Go X.509 parsing and verification](https://pkg.go.dev/crypto/x509)
- [Kubernetes TLS Secrets](https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets)

See [ROADMAP.md](../ROADMAP.md) for the intended next increments and scope.
