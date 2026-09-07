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
