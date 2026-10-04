# Color and plain output

`version` and `info` use color only when stdout is a terminal. Redirected output
and `TERM=dumb` output are plain. A nonempty `NO_COLOR` disables automatic color
for both these commands and the interactive app; an empty value preserves
automatic behavior.

In the interactive app, `NO_COLOR` takes precedence over any skin. It keeps
terminal positioning, cleanup, bold, underline and reverse selection, while
using the terminal's default foreground/background. Status and unavailable
reasons remain words. No color override flag is currently provided. To restore
skin colors, launch with `NO_COLOR` unset or empty.
