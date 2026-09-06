# K9s Flux and reliability review

The fork starts at `2d3ccc6`. It is an unchanged ancestor of upstream `84852e6`, so the working branch first incorporates those 21 upstream commits by fast-forward. The user's request authorizes fixes, performance work and useful Flux/plugin features on a reviewable branch.

## Design

Keep k9s's Go UI, resource discovery, Kubernetes connection, informer cache and custom column system. Add a native `:flux` resource browser combining discovered Flux kinds, with Enter opening the selected kind/resource. Native per-kind views show health, suspension, revision, source and the controller's condition message. A relationships action navigates source and dependency references. OCIRepository and HelmRelease direct OCI chart references are first-class inputs. Status must not present an old Ready condition as current health after a spec generation change.

Use Kubernetes GET/PATCH for explicit reconcile, suspend and resume actions on supported core Flux controllers. Confirm mutations, respect read-only mode and API permissions, carry resource version preconditions, capture the connection and selection for execution, and use timeouts. Do not require shell tools for native features. Keep optional Flux CLI/plugin integrations for richer controller-specific operations. Make denied resource kinds visible in the combined view while preserving accessible results. Read through existing informer caches; do not query every resource individually on each refresh.

Keep changes to table internals focused on reproduced boundary/index bugs and measured allocation costs. Repair Flux plugin target/context handling and read-only mutation flags. Preserve upstream history, existing shortcut conventions and opt-in plugin installation. Document feature limits and review evidence instead of claiming exhaustive absence of bugs.

## Verification

Run the full upstream test suite and build. Add regression tests for table boundaries/indexes, native Flux status/source interpretation, actual dynamic-client mutation requests, denied/missing resource kinds, registration and read-only actions. Use fake Kubernetes clients and executable stubs; no live cluster writes are part of this review. Benchmark the changed table path before and after. Independently review the final diff and report any build/runtime limitations.
