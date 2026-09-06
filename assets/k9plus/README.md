# k9+ terminal captures

These are fresh captures of the running k9+ application with its own wordmark,
`k9+ Rev: v0.1.0-dev` header, and `k9plus` runtime directories. The PNGs contain
only actual terminal cells from a PTY. Resources come from a disposable localhost
Kubernetes fixture; no user cluster or external controller is contacted.

The seven overview/detail screenshots are directly in this folder. `capture.json`
records their checks, timestamps, and binary SHA-256. `reconciling.png` is one
additional status screenshot from a separate 10,000-HelmRelease smoke;
`inline-smoke.json` records that run's assertions and provenance. Repeated raw
captures and video were omitted; historical media remains intact.

## Reproduce

From the repository root, build the current app and capture the seven views:

```sh
go build -ldflags '-X github.com/derailed/k9s/cmd.version=v0.1.0-dev' -o /tmp/k9plus-demo .
python scripts/capture-demo.py --binary /tmp/k9plus-demo --app k9plus --output assets/k9plus
python scripts/flux-inline-demo.py --binary /tmp/k9plus-demo --app k9plus --no-video --output /tmp/k9plus-inline-demo
```

Requirements: Python 3, pyte, Pillow, DejaVu fonts, and ffmpeg for video. Use
`--no-video` on the inline harness to run assertions and create screenshots only.
Both harnesses accept `--app k9s` for an earlier fork binary. That selects the
older environment/storage names while using the same fixture. Upstream K9s does
not provide the fork's native Flux and certificate workflows.

The performance harness preserves upstream comparison support:

```sh
python scripts/perf-demo.py --upstream /tmp/k9s-upstream --fork /tmp/k9plus-demo
```

It defaults to `k9s` for `--upstream`/`--before`, `k9plus` for `--fork`, and writes
new results to `assets/k9plus/performance`. Select `--fork-app k9s` to run an older
fork binary, or use the corresponding `--upstream-app`/`--before-app` overrides.
Historical result files retain their original build labels when re-rendered.

## Scope of the inline smoke

The native reconcile key wins over a loaded legacy overriding plugin. Cancellation
issues no object GET or PATCH. Both confirmed actions use one GET and one PATCH
each, and the entire 10,000-row view uses one initial-events watch and zero lists.
The sentinel CLI executable is never invoked. Filtering and row navigation remain
available while the PATCH response is held and while controller progress is held
separately. Duplicate submissions are suppressed, and watch acknowledgments show
both `Ready` and `Failed` outcomes.

The fake API's PATCH response and controller transitions wait for explicit harness
signals. These checks demonstrate UI behavior with a synthetic fixture; they do
not measure actual Flux controller speed. The existing video in
`assets/flux-inline` remains a historical pre-rebrand capture. A separate one-sample
upstream/k9+ performance-harness run was used only to verify runtime compatibility,
not to establish a new performance comparison.

All three harnesses clear inherited `K9PLUS_*`, `K9S_*`, and XDG settings, select an
explicit loopback kubeconfig, and isolate `KUBECACHEDIR`. k9+ still uses the `k9s:`
YAML root for explicit configuration-copy compatibility. Historical files in
`assets/screenshots`, `assets/flux-inline`, and `assets/performance` were retained.
