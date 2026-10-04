# Read-only linear task reports

`k9plus task` produces ordered plain text or versioned JSON without raw mode, cursor control, alternate screens, prompts, or optional provider execution. Use it with pipes, screen readers, `TERM=dumb`, and `NO_COLOR`. The existing `--headless` flag still controls TUI chrome; it is not linear output.

```sh
k9plus task workspace --context staging --namespace apps,payments --kinds pods,deployments --selector app=api
k9plus task investigate pods api-123 --context staging --namespace apps --output json --deadline 10s
k9plus task evidence ./incident.json --output text
```

Live tasks require an explicit context and namespace subset. Resource selection uses the existing curated workspace allowlist of plural aliases or full GVRs (such as `pods` or `apps/v1/deployments`); Secret reads and cluster-wide inventory are excluded. Workspace reports reuse the bounded collector (192 queries, four pages per query, 2,000 resources, 30-second default total deadline). Investigation reads one named resource and at most 100 retained UID-matching Events. It does not collect logs, metrics, arbitrary related resources, or historical activity. Coverage lists these omissions. Complete means all requested reads completed; it is not a health verdict or comprehensive diagnosis.

Reports retain captured destination, resource UID where observed, source, observation time and coverage. Investigation facts label CURRENT state and PREVIOUS termination separately. A prior OOM is not an asserted cause of a current crash. Workspace findings omit raw resource bodies. Offline evidence preserves original timestamps and performs no API calls; replay does not refresh it.

JSON schema version 1 uses `version`, `task`, `context`, `namespaces`, optional `kinds`/`selector`, `observedAt`, `complete`, `coverage`, `facts`, `limits`, and optional sanitized `evidence`. Coverage rows contain source/state/detail; facts contain subject/state/detail and optional captured resource identity. Output is capped at 4 MiB, and retained bundles keep their existing stricter bounds. Terminal controls and common credential-shaped text are sanitized. Redaction is heuristic: review notes and exported content before sharing.

Exit status is 0 for complete requested evidence, 2 when a usable report has denied, absent, timed-out, truncated or incomplete evidence, and 1 for invalid input, setup failure, or invalid/oversized files. `--allow-partial` returns 0 for a usable partial report while retaining `complete: false` and its coverage. Reports go to stdout; diagnostics go to stderr. No command automatically installs or invokes a provider. Configured Kubernetes credential helpers may execute during explicit live reads; setup is staged under the deadline and late results are discarded, but an uncooperative configured helper may outlive that deadline.
