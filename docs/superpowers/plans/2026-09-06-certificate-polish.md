# Certificate diagnostics and shared UI polish implementation plan

**Goal:** Polish shared UI elements throughout k9s, make Flux health easier to scan, and add native cert-manager diagnostics and issuance navigation.

**Architecture:** Extend existing native resource renderers and browsers. Read certificate status from unstructured objects in the informer cache; resolve relationships through discovery and owner identities. Keep renewal in the optional cmctl plugin.

**Tech stack:** Go 1.25.8, existing client-go/tview, YAML plugins. No new runtime dependencies.

**Approved scope:** User approved the first pass proposed in chat: visual polish, certificate dashboard, issuance navigation, and plugin fixes. The user clarified that the polish should apply throughout k9s while preserving its layout and workflows. Endpoint TLS probing and consumer mapping are later work.

## Constraints

- Reuse the existing PR branch; preserve upstream history and existing custom view settings.
- No live-cluster operations. Certificate lists must not read Secret data.
- Distinguish controller state from actual certificate expiry; unknown timestamps remain unknown.
- Follow explicit issuer references and owner UIDs, never infer issuance relationships from similar names.
- Native reads remain useful without cmctl. Plugin writes honor read-only mode and current context/kubeconfig.

## Tasks

- [x] Pure certificate interpretation in `internal/certmanager/resource.go`: regression cases for expiry boundaries, stale generations, renewal, request approval, ACME states, and issuer scope. Run `go test ./internal/certmanager` before/after implementation.
- [x] Native certificate rendering in `internal/render/cert_manager.go` and model/view registrations: columns for status, expiry, renewal, issuer and Secret; theme colors and custom columns. Verify renderer and model integration tests.
- [x] Issuance navigation in `internal/view/cert_manager.go`: navigate explicit issuer/Secret/owner references and UID-matched child requests/orders/challenges. Report unavailable discovery and denied list access without silently hiding them. Test identity, namespace, permissions and read-only bindings.
- [x] Flux polish: severity colors, problem-first STATUS sorting, wide-only redundant/detail fields, and a full selected-status dialog. Verify custom column and comparator regressions.
- [x] Harden `plugins/cert-manager.yaml`: confirmation, dangerous flag, safe positional values, selected kubeconfig/context, and pipeline failures. Execute substituted scripts with stub commands in regression tests.
- [x] Shared UI polish: responsive shortcut descriptions and selection dialogs, current-view breadcrumbs, Unicode-aware table padding, and safe literal status text. Verify actual simulation-screen output at narrow and wide sizes.
- [x] Run focused and full tests, configured lint, race checks on pure state handling, build and a local demo TUI capture. Independently review significant changes and resolve findings.
- [x] Update documentation/screenshots and prepare the verified tree for publication to the existing draft PR.

Verification commands use `PATH=/opt/k9s-toolchain/go/bin:$PATH GOTOOLCHAIN=go1.25.8 GOMAXPROCS=4`; full tests run with `go test -count=1 -p 2 ./...`, lint with `golangci-lint run --timeout=5m`. Capture screenshots against the local synthetic API and state that provenance in the README.
