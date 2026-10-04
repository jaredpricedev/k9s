# Stream status and narrow layouts

Log and Hubble views keep a compact three-row status at 40–109 columns. `S` opens the full status, captured scope and collection limits; `Esc` returns to the retained entry or previous view. A task viewport needs 40×12 cells (a 40×16 terminal in compact mode). Smaller viewports show a resize notice and preserve the retained selection.

Log status names LIVE, FROZEN or HISTORY, safe/raw display, visible counts, collection state and recording state. Loss counters `e`, `t`, `o`, and `h` mean memory evictions, truncated entries, forced ordering, and histogram drops. Recording failures retain a visible durability warning; the full status provides the error and disk admission/eviction counters.

At narrow widths, source comparison shows one retained lane. `Tab` switches A/B and `Enter` opens its full entry. Resizing never adopts new entries into that retained comparison.

Hubble status distinguishes unknown Relay coverage and unobserved loss from zero. The compact conversation table retains verdict and identity; `Enter` opens full flow evidence. Loss is Relay-reported and local evictions are separate. Missing observations do not establish traffic health. L7 text uses existing redaction; review exported content before sharing.
