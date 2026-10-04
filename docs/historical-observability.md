# Explicit Pod historical evidence

`:history-review` captures the selected native Pod context, GVR, namespace, name,
and UID. Opening it performs no Kubernetes or provider request. The source form
requires a caller-selected Prometheus HTTP(S) base URL and explicit UTC RFC3339
start/end times. The defaults are the previous hour; only Request submits. `r`
reopens that form. Ordinary browsing requires no provider.

The adapter issues at most two GET query_range requests, for
container_cpu_usage_seconds_total and container_memory_working_set_bytes with
exact escaped namespace and Pod matchers. CPU is cumulative seconds, not a rate,
cost estimate or waste judgment. Endpoint-to-cluster mapping is unverified.
Pod-name series cannot prove historical UID continuity; the selected UID remains
provenance, not a provider identity assertion.

Limits: eight-second collection deadline, no redirects or inherited proxy,
1 MiB per response, 32 total series, 4096 total samples, a positive window of at
most 24 hours and a step of at least 15 seconds chosen to request at most 256
sample times per series. Dates outside the representable nanosecond Unix range
are rejected before requests to prevent timestamp overflow. Requests honor cancellation. URL userinfo, query and
fragment are rejected before sanitizing. Authentication/login and arbitrary
queries are unsupported. Provider errors are redacted; no raw objects, bodies,
additional metric labels or credentials are retained. No Secret API reads occur.
Only namespace, Pod, container and finite in-window timestamp/value pairs enter
retained evidence. Wrong-target, invalid, out-of-window or nonascending samples
are rejected and counted as gaps; oversized responses fail. Exact queries,
source URL, requested window, step and observation time remain visible. A
successful matrix still has partial/unknown coverage; empty is not healthy or
complete. Denied, absent, timeout and unavailable states are distinct. A failed
refresh retains prior evidence. Destination or generation changes discard late
replies. The native view uses wrapping and scrolling at 80 and 40 columns,
without additional nested panels; search remains the existing detail search.

This is bounded Prometheus groundwork for optional issue #70. Operator demand,
review-task validation, live endpoint integration, log/trace providers and
release-history linking remain unmeasured or unsupported. Fixtures establish
bounds and lifecycle behavior, not demand or production usability. Issue #70
must remain open until its demand and broader acceptance gates are validated.
