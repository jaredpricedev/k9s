# Semantic presentation and resource tables

K9s uses neutral text for names and values, separate category colors for source identity, and health accents for status. Status words, readiness counts, restart counts and an ASCII `>` selection marker carry meaning even without color. Selected rows keep their status foreground on a subdued surface. Resource identity in the model remains complete when a displayed name is shortened.

## Resource tables

Pod tables reserve full status, readiness and restarts before allocating space to names. Namespace is retained when viewing all namespaces. Metrics, age and node appear when there is room. Names shorten first. `Ctrl+W` restores the complete column set for horizontal inspection, and resizing recovers the initial column position while preserving selected resource identity.

## Semantic skin tokens

Existing skins remain supported. An optional `k9s.semantic` object can override `canvas`, `panel`, `text`, `muted`, `focus`, `healthy`, `warning`, `failure`, `progress`, `unknown`, `category`, and `selected`. These tokens also feed inherited prompts, dialogs, help, tables and charts. Runtime updates keep selection and stream state.

Open native dialogs update their bodies, fields, choices and focused buttons without resetting edits. Dismissal, replacement and view teardown release their listeners. The pinned terminal widget keeps native form border titles aqua; list dialog titles follow the skin.

Omitted tokens fall back to existing fields: canvas/text use body colors, panel uses dialog background, muted/unknown use completed status, focus uses focused border, healthy/warning/failure/progress use modify/pending/error/add status colors, category uses YAML key color, and selected uses the table cursor background. Explicit terminal-default colors remain owned by the terminal. On custom RGB surfaces, insufficiently contrasting foregrounds are adjusted toward black or white while retaining hue where possible.

The bundled `high-contrast` and `monochrome` skins provide additional presentations. Enable `k9s.ui.noIcons` for text-only identity and ASCII selection/mark/sort/change indicators. All presentations keep explicit status labels.

Stock ordinary/selected text and repaired button, help and chart-focus pairs are tested against a 4.5:1 sRGB contrast benchmark, including 256-color quantization. The terminal controls font, palette and rendering; this benchmark is not a universal terminal accessibility certification.
