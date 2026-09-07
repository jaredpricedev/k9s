# Resource Investigation Implementation Plan

**Goal:** Start the three areas in ROADMAP.md without slowing basic browsing.
**Architecture:** Command-only entry points use the selected resource. An action
picker delegates to existing handlers; asynchronous read-only snapshots reuse
Details. Pure certificate and resource-summary functions separate rendering from
API access. No new third-party dependencies.
**Spec:** ROADMAP.md, active first increments.

- [x] Navigation: create action_palette.go; :actions snapshots registered visible
  actions, filters by typed text, Esc restores the prior list and Enter invokes
  the current registered handler only after checking the resource selection.
  Preserve read-only restrictions and existing confirmation dialogs.
- [x] Troubleshooting: create resource_inspector.go; capture selected GVR/path,
  fetch off the UI thread with a deadline, render only owner references,
  conditions and container state/restart fields; list events by involvedObject.uid.
  Show permission failures and truncated event results, not empty success.
- [x] TLS: create internal/inspect/certificate.go; PEM-decode certificate blocks,
  parse with crypto/x509, report validity, subject, issuer, SANs, SHA256 fingerprint
  for every certificate. Reject empty/malformed data without echoing bytes.
  Never parse/display tls.key; no trust or served-endpoint claims.
- [x] Verification: generated certificates cover valid/expired/not-yet-valid,
  malformed PEM and multiple certificates; UI tests cover literal markup; the lab covers action filtering. Build, focused tests and incremental lint; exercise lab
  command entry points without changing existing networking.
- [x] Publish a separate branch/PR stacked on native Hubble; keep both unmerged.

## Completion increment (PR #6)

- [x] Actions: show context/selection and categories, multi-word search, explicit no-results state; preserve the bound selection and current permissions on invocation; make Help actionable.
- [x] Inspectors: r refresh and g related-resource picker, cancellable requests and generation guards; preserve snapshot on navigation and restore it on return.
- [x] Troubleshooting: controller selector-scoped pod diagnostics, owner/pod jumps, sorted retained events with modern timestamp fallback; bounded lists and explicit gaps.
- [x] TLS: Certificate/Ingress/Gateway references to Secrets, same-namespace reverse consumers; no implicit cross-namespace certificate fetches. System or explicit local CA trust verification with explicit hostname. Explicit tlsprobe host:port serverName [ca=/path] performs a verified TLS handshake from k9plus, no HTTP request.
- [ ] Verification: fake-client scope/permission/partial-list checks, generated PKI and local TLS server tests, navigation and literal-markup regressions, direct-launch lab workflow. Update usage, roadmap and PR; do not merge.
