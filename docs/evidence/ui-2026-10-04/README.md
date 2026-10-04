# Actual terminal captures — 2026-10-04

All 12 capture checks passed at 80×24: stock, high-contrast and monochrome skins, each in true-color and 256-color modes, showing the Pod table and F2 destination panel. The app ran in read-only mode with no icons against a disposable Kubernetes API fixture bound to `127.0.0.1`. Every workload, context, cluster and user name is synthetic.

PNG content comes only from actual PTY terminal cells emitted by the app and rasterized with `pyte` and Pillow. The adjacent TXT files preserve the captured terminal text. `TCELL_TRUECOLOR=disable` forced the 256-color runs; these images are actual terminal captures, separate from the simulation-screen palette approximations described in [presentation.md](../../presentation.md).

The [original manifest](manifest.json) is copied byte-for-byte, preserving its timestamps, filenames, expected visible strings and recorded binary hash. The captures were taken at 00:55:24–00:55:33 UTC from `/workspace/k9plus-final`, originally written to `/workspace/k9plus-evidence/skins`.

Captured binary SHA-256: `41ebaf355805effdabab6f7bdf21353cd6ddc7e755edf779c0e2e6e18584267e`.

These images document that captured binary. Later filtering/performance changes were not recaptured here, so this directory does not establish the appearance or behavior of a later source snapshot or binary. It contains no human operator research or live-cluster rollout evidence.

| Skin | Terminal mode | Pod table | Full destination |
| --- | --- | --- | --- |
| stock | true-color | [PNG](pods-80x24-stock-true-color.png) · [TXT](pods-80x24-stock-true-color.txt) | [PNG](pods-80x24-stock-true-color-destination.png) · [TXT](pods-80x24-stock-true-color-destination.txt) |
| stock | 256-color | [PNG](pods-80x24-stock-256.png) · [TXT](pods-80x24-stock-256.txt) | [PNG](pods-80x24-stock-256-destination.png) · [TXT](pods-80x24-stock-256-destination.txt) |
| high-contrast | true-color | [PNG](pods-80x24-high-contrast-true-color.png) · [TXT](pods-80x24-high-contrast-true-color.txt) | [PNG](pods-80x24-high-contrast-true-color-destination.png) · [TXT](pods-80x24-high-contrast-true-color-destination.txt) |
| high-contrast | 256-color | [PNG](pods-80x24-high-contrast-256.png) · [TXT](pods-80x24-high-contrast-256.txt) | [PNG](pods-80x24-high-contrast-256-destination.png) · [TXT](pods-80x24-high-contrast-256-destination.txt) |
| monochrome | true-color | [PNG](pods-80x24-monochrome-true-color.png) · [TXT](pods-80x24-monochrome-true-color.txt) | [PNG](pods-80x24-monochrome-true-color-destination.png) · [TXT](pods-80x24-monochrome-true-color-destination.txt) |
| monochrome | 256-color | [PNG](pods-80x24-monochrome-256.png) · [TXT](pods-80x24-monochrome-256.txt) | [PNG](pods-80x24-monochrome-256-destination.png) · [TXT](pods-80x24-monochrome-256-destination.txt) |

The Pod views visibly retain `CrashLoopBackOff`, `ContainerCreating`, `0/1` readiness, 7 restarts, shortened-name ellipses, the ASCII selection marker and the read-only destination strip. The F2 panels show complete context, namespace, mode, connection, cluster, user, metrics source and unavailable reason. Automated expected-string checks for each capture remain in the manifest.

To produce a new, separately attributable capture set:

```sh
python scripts/skin-journeys.py --binary /path/to/reviewed-binary --output /tmp/new-ui-evidence
```

Run that command from the repository root with the dependencies in `scripts/terminal-requirements.txt`. Retain the new manifest and binary hash with the resulting images.
