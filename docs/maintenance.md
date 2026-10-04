<!-- Modified for k9+; see NOTICE. -->

# Maintaining the fork

The compatible Go module path and YAML root remain upstream interfaces. The binary,
storage directories, packaging and displayed identity belong to k9plus. Retain
Apache attribution, source notices and the dependency licensing bundle described in
[licensing](licensing.md); record changes in [MODIFICATIONS](../MODIFICATIONS.md).

Fork-owned seams include `internal/flux`, `internal/certmanager`, `internal/logstream`,
`internal/hubble`, `internal/inspect` and their on-demand view adapters. Keep ingestion,
redaction and frozen evidence in their domain packages. Ordinary list browsing must
not run integration or relationship discovery. UI adapters own keyboard interaction
and cancellable delivery; pinned clients and captured identity own API destinations.

For an upstream update, create an isolated branch from current master, fetch the
upstream revision and record the old/new comparison points. Review upstream changes
to shared client, model and UI seams before cherry-picking or merging. Resolve conflicts
against fork contracts, preserving fast keys, read-only/RBAC checks, canonical resource
identity and bounded evidence. Keep independent feature changes out of the sync PR.

Run the ordinary Go suite, focused race suites, lint, license collector tests and
distribution license bundle. Exercise the disposable API/PTY journeys, narrow layouts,
skins/no-icons and slow API responsiveness. Capture the exact commit, toolchain,
workload, hardware and date; distinguish fixtures from opt-in live Kubernetes/Relay
checks. Update the feature matrix and modification inventory before release. Open a
reviewable PR; merging and publishing require explicit authorization.
