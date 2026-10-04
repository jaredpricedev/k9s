# Read-only task handoffs

`:taskbook save` builds a handoff from an available evidence preview, retained
inspection/comparison, daily workspace snapshot, or open taskbook. It performs no
new capture. Use `:evidence` first for an explicit resource/event capture; selected
redacted log/flow text can be added to that preview with a named source and its
coverage limits. Daily workspaces contribute at most eight retained resource
summaries and bounded query coverage, never raw `Resource.Object` values.

The save form records a concise summary, unresolved questions and evidence links
(separate entries with `|`). Optional fixed client checks are `kubectl-version`,
`helm-version`, `flux-version`, and `cilium-version`; these add only predefined
version invocations. Resource checks use the retained context/GVR/namespace/name
and UID. No executable or argv can be supplied by an imported artifact. Save
creates a new absolute `.json` file with mode 0600 and refuses overwrite.

`:taskbook open /absolute/path.json` opens a bounded version 1 artifact offline.
It never dials the API, discovers providers, switches context, opens links, runs
checks, recaptures streams, authenticates, applies or mutates resources. Imports
reject unsupported versions, unknown fields/check IDs, invalid target identities,
missing evidence, excessive collections and files over 4 MiB. Original evidence
retains its original identity, source, time, coverage and replay limits.

Press `r`, then **Rerun checks**, to explicitly run the listed reads. API GETs
require a saved UID and the recorded current context; replacement or missing
objects are stale, and replacement bodies are excluded. Missing UIDs and excluded
Secret kinds perform no API request. Provider checks use fixed argv without a
shell and the captured destination; unavailable, absent and denied providers
remain explicit states. Reads default to eight seconds per check, and client
version evidence is bounded to 8 KiB. Raw provider stderr and errors are never
retained or displayed. Cancellation and destination revision guards reject late
results. Original evidence stays separate from latest explicitly rerun results.
Press `s` to save an updated handoff to another new file.

The interface uses a single scrollable text pane and keyboard forms at 80×24,
60×18 and 40×16. `Tab` moves between form fields/buttons, `PageUp/PageDown` scrolls
form instructions, and `Escape` cancels. `q`/`Escape` returns to the retained owner
and cancels pending checks without changing its context, scope, query or selection.
Below supported form dimensions, resize instructions replace fields. Status text
remains meaningful without color or icons.

Secret bodies are excluded before reads and sanitized on import/export. Sensitive
field names and recognizable credentials are redacted heuristically; unfamiliar
credentials or operational data may remain. Review the preview, including selected
snapshots and text, before sharing. Frozen snippets do not establish full log/flow
history; workspace/API observations are not atomic cluster snapshots. Evidence
links are operator references, not automatically verified dependencies.

Tests use synthetic API/provider adapters and terminal simulation screens. They
verify guards and offline behavior; they do not claim live-cluster integration or
operator-study results.
