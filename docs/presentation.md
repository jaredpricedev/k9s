# Presentation and compact terminals

k9+ uses neutral text for names and values, separate category colors for source identity, and health accents for status. Status words, readiness counts, restart counts and an ASCII `>` selection marker carry meaning even without color. Selected rows keep their status foreground on a subdued surface. Resource identity in the model remains complete when a displayed name is shortened.

![Actual 80×24 Pod table capture in stock true-color mode](evidence/ui-2026-10-04/pods-80x24-stock-true-color.png)

This actual PTY capture uses synthetic names and a disposable loopback API fixture. The [12-capture evidence index](evidence/ui-2026-10-04/README.md) includes all three skins, both terminal color modes, full destination panels, raw terminal text and the unchanged manifest. Its recorded binary hash is `a28a1f48b72d255348ad72bc4124d5973cc003a4070520c81743bd668aa57747`; the clean final capture was built from source commit `76ac522ff90ce3ddf25d320e911c55c7720b818a` and includes the queued-filter correction.

## Terminal size and actions

`k9s.ui.headerMode` supports `auto` (the default), `compact`, and `full`. Automatic mode displays compact chrome below 120 columns or 34 rows. `Ctrl+E` makes a manual choice that remains in force across resizes. The destination strip stays visible in both modes, in prompts, and while borderless detail/log views expand within the content area. `F2` reveals the complete destination. Compact hints retain complete primary labels and `Ctrl+O` opens searchable actions; `?` opens help. Omitting a hint does not remove its action.

Pod tables reserve full status, readiness and restarts before allocating space to names. Namespace is retained when viewing all namespaces. Metrics, age and node appear when there is room. Names shorten first, and width left over after the other columns are placed is returned to the name. `Ctrl+W` restores the complete column set for horizontal inspection, and resizing recovers the initial column position while preserving selected resource identity.

## Health summary and problems first

A resource list with a status column states how many rows need attention beside its row count, for example `pods(all)[6] 2 failing 1 degraded 1 in progress`. The counts use the same classification that colors the cells. A status word decides a row; a readiness count decides only when no status column speaks; restart counts are history and are not counted. Nothing is added when no row needs attention, and parts are dropped from the least severe when the frame is too narrow.

Failing, then degraded, then in-progress rows are listed ahead of the rest by default, keeping the chosen column order inside each group and the selected resource. The title states `problems first` while a list has rows needing attention. `Shift+X` switches every list to plain column order for the session, and back.

## Cause and trend

Pod lists add a `WHY` column: one phrase taken from the Pod status for a pod that is not running cleanly — the previous exit behind a back-off (`api: last OOMKilled exit 137`), a waiting or scheduling message, an eviction message, or the container that is running but not ready. It reports status evidence only and is empty for a healthy pod. The column is shown from 96 columns when at least one row has a cause; it claims a readable minimum, lets age and usage be placed, then grows into unused width. `Ctrl+W` always shows it in full.

While a list is open it remembers up to eight samples of each row's `CPU` and `MEM` values, taken at most every two seconds, and draws them as a sparkline before the number, scaled from zero to that row's maximum. `RESTARTS` shows the restarts gained since the list was opened as `+N`. This is session memory, not cluster history: it starts empty, is never stored, ignores unavailable values and is dropped when the resource leaves the list. With `noIcons`, the sparkline is replaced by `+`, `-` or `=` comparing the newest sample with the oldest. Sorting, filtering and export use the unchanged values.

## Next step line

When the selected row of a resource list reports a problem, the bottom edge of the table frame carries one line: `next:`, the status evidence decoded, then the checks that open more evidence, each with the key or typed command that this view really runs. For example `next: api: last exit 137: killed (SIGKILL), out of memory (OOMKilled) · p previous logs · :pressure limit vs usage`. A healthy selection leaves the border plain. The line is drawn on the frame, so it never moves a row and never touches the flash line.

The line is derived only from fields the list already holds (`STATUS`, `READY` and the pod `WHY` phrase); moving the selection reads nothing from the cluster. Exit codes are decoded by convention: 137 is SIGKILL ("out of memory" when the status says `OOMKilled`, otherwise "usually the memory limit or the node"), 143 is SIGTERM (a normal stop or a failed graceful shutdown), 139 is a segfault, 126 and 127 are a command that is not executable or not found in the image, 1 and 2 are the application's own error. Other signals are named by number and other codes are called application-defined. Scheduling guidance repeats which constraint the scheduler message names (resources, taints, affinity/selector, topology spread, volumes, cordoned nodes, host ports) and points at the message itself when it recognizes none. `:capacity` leads only when the message names resources. A Terminating pod shows no line. The wording is evidence plus a proposed check, never a diagnosis.

Covered states: pod exits and back-offs, image pull failures, container configuration and start errors, eviction, unschedulable and other Pending pods, a pod blocked while creating (only when the kubelet reports a message), running but not ready; a Pending or Lost PersistentVolumeClaim; a Deployment or StatefulSet with fewer ready replicas than desired; a NotReady node. Other resources and states show no line rather than a generic one.

Every key and command is checked against the action registry behind `Ctrl-O` for the selected row before it is printed. A check that is unbound in the view, unavailable for the selection, or disabled by read-only mode is left out; the evidence stays. The mapping itself only proposes reads (logs, describe, review commands).

When the line does not fit it drops the explanatory detail from the last check back, then the decoded wording in favour of the short evidence (`last exit 137 OOMKilled`), then trailing checks, so a narrower frame never hides a check that a wider one shows only to keep longer prose. If evidence and one check still do not fit, or the frame leaves fewer than 40 cells, nothing is shown; the line never wraps or clips. Width is measured in terminal cells. Evidence is bold in the warning or failure token and shortcuts are bold in the focus token, but the words carry the meaning; with `noIcons` the separator is `|` instead of `·`.

Limits: the guidance is only as current as the list row. It parses the `WHY` phrase, so a custom view that removes `STATUS` or `WHY` from pods loses the corresponding detail. It reports the first failing container, as `WHY` does. A deployment line is based on the ready count alone and does not read conditions such as `ProgressDeadlineExceeded`; `:rollout` shows those. There is no setting to turn the line off.

## Semantic skin tokens

Existing skins remain supported. An optional `k9s.semantic` object can override `canvas`, `panel`, `text`, `muted`, `focus`, `healthy`, `warning`, `failure`, `progress`, `unknown`, `category`, and `selected`. These tokens also feed inherited prompts, dialogs, help, tables and charts. Runtime updates keep selection and stream state.

Open native dialogs update their bodies, fields, choices and focused buttons without resetting edits. Dismissal, replacement and view teardown release their listeners. The pinned terminal widget keeps native form border titles aqua; list dialog titles follow the skin.

Omitted tokens fall back to existing fields: canvas/text use body colors, panel uses dialog background, muted/unknown use completed status, focus uses focused border, healthy/warning/failure/progress use modify/pending/error/add status colors, category uses YAML key color, and selected uses the table cursor background. Explicit terminal-default colors remain owned by the terminal. On custom RGB surfaces, insufficiently contrasting foregrounds are adjusted toward black or white while retaining hue where possible.

The bundled `high-contrast` and `monochrome` skins provide additional presentations. Enable `k9s.ui.noIcons` for text-only identity and ASCII selection/mark/sort/change indicators. All presentations keep explicit status labels and action access.

Stock ordinary/selected text and repaired button, help and chart-focus pairs are tested against a 4.5:1 sRGB contrast benchmark, including 256-color quantization. The terminal controls font, palette and rendering; this benchmark is not a universal terminal accessibility certification.

## Render fixtures

The long-name Pod fixture checks 80×24, 100×30 and 120×34, selection across resizes, horizontal-scroll recovery, visible name elision, and stock/custom/high-contrast/monochrome skin changes. To export and render deterministic terminal cells:

```sh
K9S_RENDER_FIXTURE_DIR=/tmp/k9s-render-fixtures go test ./internal/ui -run TestCapturePresentationFixture
python scripts/render-presentation.py /tmp/k9s-render-fixtures
```

The export includes stock, high-contrast and monochrome presentations and runs only when that directory is explicitly configured. Each skin has separately named `true-color` and `256` JSON/PNG files; the latter approximates xterm palette quantization. These are simulation-screen fixtures, not live-cluster captures or human operator research. The renderer requires Pillow and fixed DejaVu Sans Mono font files. It adds no labels or UI text to the emitted cells.

To exercise actual terminal capability negotiation and the full destination dialog against a disposable local API fixture:

```sh
python scripts/skin-journeys.py --binary ./execs/k9plus --output /tmp/k9plus-skin-journeys
```

This helper uses `pyte` and Pillow, isolated application directories, read-only mode, no icons, and all three skins at 80×24. It explicitly enables true-color or disables it with `TCELL_TRUECOLOR=disable` for the 256-color run. Its manifest records the binary hash and each capture’s mode and source. Assertions wait for complete visible states; the PNG renderer uses only the app’s emitted terminal cells.
