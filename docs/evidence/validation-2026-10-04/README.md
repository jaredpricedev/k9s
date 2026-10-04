<!-- Modified for k9+; see NOTICE. -->

# Verification output — 4 October 2026

These are original local tool results. Go checks use Go 1.25.8 on Linux amd64;
fixture servers use explicitly permitted loopback networking. The broad ordinary
suite uses CGO disabled, and the race suite uses CGO enabled. Lint uses
golangci-lint 2.6.0. The final lint-only edit names an unused test argument `_`;
it changes no test behavior or application code.

| Check | Original result |
| --- | --- |
| Complete ordinary Go suite, CGO disabled | [go-cgo0.txt](go-cgo0.txt) |
| Eight-package race suite, CGO enabled | [go-race.txt](go-race.txt) |
| Lint | [lint.txt](lint.txt) |
| License collector regressions | [license-tests.txt](license-tests.txt) |
| 379-module distribution license bundle | [license-bundle.txt](license-bundle.txt) |
| Disposable real kind Kubernetes 1.34.0 | [kubernetes-1.34.txt](kubernetes-1.34.txt) |
| Workspace standalone + later filter checks | [workspace.txt](workspace.txt), [follow-up](workspace-followup.txt), [race](workspace-followup-race.txt) |
| Operations standalone | [operations.txt](operations.txt) |
| Hubble standalone | [hubble.txt](hubble.txt) |
| Comparison standalone | [comparison.txt](comparison.txt) |
| Diagnostics standalone | [diagnostics.txt](diagnostics.txt) |
| Pressure standalone | [pressure.txt](pressure.txt) |
| Evidence standalone | [evidence.txt](evidence.txt) |

The independent slice commits are `2836a967` (workspace follow-up), `2a583a01`
(operations), `f76f5f3a` (Hubble), `c6d018da` (comparison), `ca5a189e`
(diagnostics), `c37e471c` (pressure), and `a35c095c` (evidence). Isolated checkouts
needed `-buildvcs=false` because of execution-environment Git restrictions;
normal main-checkout builds retained the documented linker metadata.

The real Kubernetes test created a unique disposable namespace and exercised
UID/resource-version semantics, conditional writes and scoped configuration /
scheduling observations. The owned kind cluster and Docker network were removed.
Its log does not establish induced OOM, live Relay or Gateway coverage.

Actual terminal evidence is retained separately for [skins](../ui-2026-10-04/README.md),
[filters](../../filter-performance-2026-10-04.md), and
[Hubble](../../hubble-performance-2026-10-04.md). Fixtures use synthetic resource
names. Raw output padding is preserved; `.gitattributes` disables end-of-line
whitespace checks only for these raw TXT artifacts.

See [the validation guide](../../validation-2026-10-04.md) for commands, matrix and
limits. No hosted CI success is inferred from empty GitHub status responses.
