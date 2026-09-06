# Block-logo captures

These screenshots show the selected five-row `[k9+]` block wordmark in the
running application. The glyphs occupy 32 terminal columns. Brackets and the
plus use the active skin's logo accent; letters use its body foreground.
Transient status colors still apply to the accent. The stock skin shown here
uses amber and cadet blue. The header height and resource views are unchanged.

Captured from a real PTY against the disposable localhost Kubernetes fixture
in `scripts/capture-demo.py`. Resources are synthetic; no user cluster was used.
`capture.json` records the binary SHA-256, terminal size and per-view checks.
Previous screenshots and measured performance recordings remain in their
original directories.

```sh
python scripts/capture-demo.py --binary /path/to/k9plus --output assets/block-logo
```

Requires Python 3, Pillow, pyte and DejaVu fonts. Actual glyph appearance follows
your terminal font; no terminal image protocol or Nerd Font is required.
