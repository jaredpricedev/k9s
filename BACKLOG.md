# k9plus backlog

Updated 2026-09-07. These items are deferred, not active implementation commitments.
The native Hubble first increment is in [PR #5](https://github.com/jaredpricedev/k9s/pull/5), pending review and merge approval.

## Candidate next discussion: advanced TLS

Explore an advanced TLS feature for k9plus. Scope and acceptance criteria are
not yet defined. Clarify the intended investigation workflow before implementation;
do not assume this means only Hubble Relay TLS configuration.

## Native Cilium / Hubble follow-up

| Item | Intended outcome | Important boundaries |
| --- | --- | --- |
| Relay setup and recovery | Discover Relay, optionally manage port-forwards, improve connection recovery and TLS diagnostics. | Per-context configuration; explicit visibility gaps; verified TLS/mTLS. |
| Drop triage | Group observed drops by reason, endpoint and port; drill into representative events and suggested next checks. | Distinguish reported evidence from hypotheses. |
| Source/destination pin mode | Pin either endpoint and investigate a bidirectional conversation with a selected peer. | Include external peers; handle pod replacement and address changes. |
| Broader resource entry points | Open investigations from Services and Namespaces; resolve Service backends. | Preserve exact scope and clearly report missing or changing membership. |
| DNS analysis | Inspect reported queries, responses, response codes, answers and latency where available; follow answers into related traffic. | Require reported DNS visibility; account for cached/stale answers and incomplete correlation; DNS success is not policy authorization. |
| Policy links and inspection | Jump from explicit attribution to Kubernetes/Cilium policies; inspect selectors and rules. | Separate Hubble-reported attribution from configuration-based candidate matches. |
| Saved captures and safe export | Save frozen datasets, reopen offline, retain filters, observation windows, coverage and loss metadata. | Redaction, restrictive file permissions, retention and a versioned format; no implicit raw L7 export. |
| Application relationships | Group workloads into applications and explore observed dependencies, including cross-cluster relationships. | Define ownership/grouping rules and reliable cluster identity; observed dependencies are incomplete. |
| Reviewed policy proposals | Draft policies from selected evidence, explain scope, compare existing rules and export YAML or prepare a Git review workflow. | Explicit review before application; account for rare jobs, failover and incomplete capture windows; never silently alter policies. |
| Filter and navigation improvements | Expand supported structured filters, discoverable syntax, conversation styling and navigation. | Plain text remains local search; malformed structured input preserves the previous filter; stable frozen selection. |
| Compatibility and scale verification | Exercise real DNS/L7, policy attribution, TLS configurations, multiple Cilium versions, large scopes and long-running observations. | Preserve the logging lab; use suitable separate private labs and report real limitations. |

Suggested Hubble sequence when resumed: setup, drop triage, pin mode and Service
entry points; then DNS, policy inspection and saved captures; then application
relationships and reviewed policy proposals. This sequence is tentative; advanced
TLS may be discussed first.

All increments retain observed-event count semantics, redaction, explicit
history/live/frozen states and separate Relay loss/local eviction indicators.
Do not automatically enable L7 visibility. No merges without user approval.
