# Certificate diagnostics

Open `:certificates all` for cert-manager Certificates across namespaces, or `:certificates <namespace>` for a narrower view. Native views also cover CertificateRequests, Issuers, ClusterIssuers, ACME Orders and Challenges. They use discovered resources and the existing watch cache; no cmctl installation or Secret read permission is required to list Certificates.

## Read certificate health

The Certificate table shows status, expiration and renewal timestamps in UTC, issuer and target Secret. Expiration sorting puts earlier timestamps first. Wide mode exposes DNS names, the full controller message and age; press `i` for a scrollable status summary without widening the table.

- **Expired** means the reported `notAfter` has passed, even if the last Ready condition still says true.
- **Expiring** means the certificate is in the final 10% of its reported validity period. This scales to short-lived certificates.
- **Renewal Due** means cert-manager's reported renewal time has arrived; it does not, by itself, prove a renewal failure.
- **Issuing / Renewing** reflect active issuance. An expired certificate remains Expired while renewal is underway.
- **Pending / Unknown** distinguish stale or incomplete controller observations from confirmed health.
- Requests expose approval, denial and failure states. ACME Orders distinguish readiness to finalize from completed validation; Challenge reasons remain available in the status summary.

These are resource-status diagnostics. They do not validate a certificate chain, read private keys, or prove which certificate a network endpoint is serving.

## Follow issuance

Press `g` to select a related resource. The browser follows explicit issuer and Secret references, cert-manager owner references, and cached child resources:

| Selected resource | Child resources |
| --- | --- |
| Certificate | CertificateRequests |
| CertificateRequest | ACME Orders |
| Order | ACME Challenges |

Children must match the parent's namespace, API group, kind and UID. Similar names alone do not establish a relationship. The issuer's actual discovered scope controls navigation, including custom issuer groups. Opening a referenced Secret uses the normal Secret browser and its normal RBAC checks.

The first child lookup may start an informer. If its cache is still loading, close the relationship list and press `g` again after synchronization. Missing resource types and denied child-list permissions appear as informational entries; they do not imply that no related resources exist. Non-ACME issuers may have no Orders or Challenges.

## Optional cmctl actions

Install cmctl separately and copy `plugins/cert-manager.yaml` into the plugin directory reported by `k9s info`.

| Key | Action |
| --- | --- |
| `Shift-S` on a Certificate | Detailed cmctl certificate status; overrides the normal status-sort shortcut |
| `Shift-R` on a Certificate | Confirm renewal of that Certificate; disabled in read-only mode |
| `Shift-I` on a Secret | cmctl Secret inspection |

The scripts forward the selected context and kubeconfig as data, and propagate command and pager failures. Renewal confirmation names the target and context. Native navigation and diagnostics remain available without this plugin.

## References

- [Certificate lifecycle](https://cert-manager.io/docs/usage/certificate/)
- [API status fields](https://cert-manager.io/docs/reference/api-docs/)
- [Issuance troubleshooting](https://cert-manager.io/docs/troubleshooting/acme/)
- [cmctl](https://cert-manager.io/docs/reference/cmctl/)

Validation uses synthetic objects, fake commands and a local demo API. Real issuer behavior and live-cluster permissions require an environment-specific smoke test.
