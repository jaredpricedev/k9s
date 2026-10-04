# Explicit provider checks

Use `:providers` to choose checks. Opening this help or browsing resources does
not scan tools, run optional integrations, install programs, log in, or change
the current Kubernetes context.

Examples:

- `:providers git helm kustomize` checks only these client versions.
- `:providers kubectl` runs the client-only version command.
- `:providers api:metrics api:flux` checks bounded read prerequisites in the
  captured context and namespace.
- `:providers api:resource` checks the selected object's captured UID.

The view distinguishes available, absent, denied, incompatible and unavailable
observations. `r` repeats the explicit check; `Esc` cancels and returns. Retained
observations display their destination and time. A destination change requires
reopening the checks. CLI availability and API read permission do not establish
workload health or authorize mutation.

`internal/provider` supplies shared contracts for adapters:

- `Scope` captures context, workspace namespace, selected target namespace,
  GVR, name, UID and destination revision.
- `Input` captures exact executable/argv, directory, optional environment and
  execution limits. `Run` never invokes a shell or exposes global app state.
- `Result` carries the captured scope, executable, timestamps, exit code, bounded
  stdout/stderr, truncation and execution state.
- `Spec` declares an explicit client-version probe or a context-aware API probe.
  `Discover` checks only the supplied specifications and applies optional
  compatibility validation.

Run adapters on a worker and apply results through the UI dispatcher only while
the captured destination and request generation remain current. API adapters
must use independent captured connections and context-aware requests. Canceled
API replies are discarded. Unix cancellation kills the isolated process group;
Windows cancellation kills the child and bounds inherited output pipes.

Default execution limits are eight seconds, 1 MiB stdout and 8 KiB stderr. Limits
cannot exceed one minute or 4 MiB per stream. Output overflow cancels the job and
is a failure, not a valid partial rendered source. Existing plugin and custom
jump configuration remains compatible; these contracts do not reinterpret its
arguments or automatically execute it. Mutation adapters must also use the
guarded operation lifecycle and its write-outcome semantics.
