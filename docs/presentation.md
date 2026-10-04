# Presentation and compact terminals

k9+ uses neutral text for names and values, separate category colors for source identity, and health accents for status. Status words, readiness counts, restart counts and an ASCII `>` selection marker carry meaning even without color. Selected rows keep their status foreground on a subdued surface. Resource identity in the model remains complete when a displayed name is shortened.

![Actual 80×24 Pod table capture in stock true-color mode](evidence/ui-2026-10-04/pods-80x24-stock-true-color.png)

This actual PTY capture uses synthetic names and a disposable loopback API fixture. The [12-capture evidence index](evidence/ui-2026-10-04/README.md) includes all three skins, both terminal color modes, full destination panels, raw terminal text and the unchanged manifest. Its recorded binary hash is `41ebaf355805effdabab6f7bdf21353cd6ddc7e755edf779c0e2e6e18584267e`; later filtering/performance changes were not recaptured in this set.

## Terminal size and actions

`k9s.ui.headerMode` supports `auto` (the default), `compact`, and `full`. Automatic mode displays compact chrome below 120 columns or 34 rows. `Ctrl+E` makes a manual choice that remains in force across resizes. The destination strip stays visible in both modes, in prompts, and while borderless detail/log views expand within the content area. `F2` reveals the complete destination. Compact hints retain complete primary labels and `Ctrl+O` opens searchable actions; `?` opens help. Omitting a hint does not remove its action.

Pod tables reserve full status, readiness and restarts before allocating space to names. Namespace is retained when viewing all namespaces. Metrics, age and node appear when there is room. Names shorten first. `Ctrl+W` restores the complete column set for horizontal inspection, and resizing recovers the initial column position while preserving selected resource identity.

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
