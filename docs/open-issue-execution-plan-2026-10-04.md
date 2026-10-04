# Open-issue execution plan — k9+

Source: [live GitHub tracker #8](https://github.com/jaredpricedev/k9s/issues/8). Implementation-source candidate [`c0e5ff82f5f965a2e630a5435795bc21d470da91`](https://github.com/jaredpricedev/k9s/tree/c0e5ff82f5f965a2e630a5435795bc21d470da91), tree `d9cff84708c0b845f3bd5fae9bde388dbad02156`, is based on master `d55c090487a72cf09a89a687bae6d0c3c70765ec` and carries PR #122 source. PR #121 is merged. See the [toolkit roadmap](toolkit-roadmap-2026-10-03.md) and [operator task study packet](operator-task-study-2026-10-04.md).

## Current status — 4 October 2026

Of 41 delivery issues, 31 are closed with merged implementations. Ten delivery
issues remain open: #22 and #23, whose implementations shipped but still require
the human operator study, plus optional issues #65–#72. All 33 core implementation
scopes exist in this source. Seven optional native foundations (#65–#68, #70–#72)
are implemented here; PR #116 includes the #71 operator and #72 dependency
foundations. Their demand, live compatibility and broader-provider validation
gates remain open. PR #121's native acceptance fixes are merged; the PR #122
source fixes the wide-Coverage primary hint. Its regression test and existing native
acceptance evidence support the nonhuman #22/#23 criteria within their documented
scope; the separate human studies remain open. Issue #69 is deferred because the user has no current on-prem
cost-review need. Tracking issue #8 is also open, for 11 open issues total. Core
change-set delivery #53 shipped in PR #118.

The phased list below preserves the earlier proposed order and all 41 delivery
issue identifiers from its original 4 October tracker snapshot. That historical
42-issue count is not the current open-issue count. Phase ordering remains a
dependency/prioritization reference, not a statement that listed issues are still
open or a schedule commitment.

## Historical proposed delivery order — original 4 October snapshot

| Phase | Milestone | Issues in recommended order | Count |
| --- | --- | --- | --- |
| 1 | Reliability and session recovery | [#73](https://github.com/jaredpricedev/k9s/issues/73), [#74](https://github.com/jaredpricedev/k9s/issues/74), [#75](https://github.com/jaredpricedev/k9s/issues/75), [#76](https://github.com/jaredpricedev/k9s/issues/76), [#49](https://github.com/jaredpricedev/k9s/issues/49) | 5 |
| 2 | Complete the UI/UX foundation | [#22](https://github.com/jaredpricedev/k9s/issues/22), [#42](https://github.com/jaredpricedev/k9s/issues/42), [#45](https://github.com/jaredpricedev/k9s/issues/45), [#23](https://github.com/jaredpricedev/k9s/issues/23), [#43](https://github.com/jaredpricedev/k9s/issues/43), [#44](https://github.com/jaredpricedev/k9s/issues/44), [#77](https://github.com/jaredpricedev/k9s/issues/77), [#78](https://github.com/jaredpricedev/k9s/issues/78), [#79](https://github.com/jaredpricedev/k9s/issues/79), [#46](https://github.com/jaredpricedev/k9s/issues/46) | 10 |
| 3 | Shared execution and provider infrastructure | [#47](https://github.com/jaredpricedev/k9s/issues/47), [#63](https://github.com/jaredpricedev/k9s/issues/63) | 2 |
| 4 | Finish Change & Release | [#50](https://github.com/jaredpricedev/k9s/issues/50), [#51](https://github.com/jaredpricedev/k9s/issues/51), [#52](https://github.com/jaredpricedev/k9s/issues/52), [#54](https://github.com/jaredpricedev/k9s/issues/54), [#53](https://github.com/jaredpricedev/k9s/issues/53) | 5 |
| 5 | Daily activity and handoffs | [#60](https://github.com/jaredpricedev/k9s/issues/60), [#48](https://github.com/jaredpricedev/k9s/issues/48), [#55](https://github.com/jaredpricedev/k9s/issues/55), [#64](https://github.com/jaredpricedev/k9s/issues/64), [#80](https://github.com/jaredpricedev/k9s/issues/80) | 5 |
| 6 | Routine engineering workflows | [#56](https://github.com/jaredpricedev/k9s/issues/56), [#59](https://github.com/jaredpricedev/k9s/issues/59), [#57](https://github.com/jaredpricedev/k9s/issues/57), [#58](https://github.com/jaredpricedev/k9s/issues/58), [#62](https://github.com/jaredpricedev/k9s/issues/62), [#61](https://github.com/jaredpricedev/k9s/issues/61) | 6 |
| 7 | Demand-gated optional integrations | [#65](https://github.com/jaredpricedev/k9s/issues/65), [#66](https://github.com/jaredpricedev/k9s/issues/66), [#67](https://github.com/jaredpricedev/k9s/issues/67), [#68](https://github.com/jaredpricedev/k9s/issues/68), [#69](https://github.com/jaredpricedev/k9s/issues/69), [#70](https://github.com/jaredpricedev/k9s/issues/70), [#71](https://github.com/jaredpricedev/k9s/issues/71), [#72](https://github.com/jaredpricedev/k9s/issues/72) | 8 |

### 1. Reliability and session recovery

Fix terminal shutdown, visible drain validation, command responsiveness and resource-health availability. Then refresh running client handles without losing the workspace.

**Completion gate:** External exits restore the terminal; invalid visible input invokes no maintenance callback; delayed discovery does not freeze input; unknown health remains unknown; session refresh cannot redirect pending work.

### 2. Complete the UI/UX foundation

Establish help/action and responsive contracts first; fix explicit revision selection; then improve investigation/workspace/diff hierarchy, stream status, Pulse geometry, error recovery and color/output behavior.

**Completion gate:** Pinned 120×34/80×24/60-column/minimum frames, keyboard Back/selection, no-icons and monochrome checks pass. Complete the existing #22/#23 operator studies before closing those tickets.

### 3. Shared execution and provider infrastructure

Migrate inherited operations to the guarded lifecycle and define typed, on-demand provider discovery/results. Reuse current operation/action/client contracts.

**Completion gate:** Captured destinations, preconditions, bounded background work, cancellation, stale-result isolation and per-target outcomes are consistent. Unknown write outcomes are never automatically retried.

### 4. Finish Change & Release

Build on PR #41's local-manifest/Deployment slice. Complete desired sources and rollout/recovery tracking, add GitOps ownership and configuration review, then reviewed change sets.

**Completion gate:** Review exact source/scope/target changes before explicit execution; distinguish local comparison/server preview and accepted writes/observed outcomes; retain partial results and recovery receipts.

### 5. Daily activity and handoffs

Compose scheduled-job outcomes, observed daily deltas and application activity, then runbooks/handoffs and plain/structured task output.

**Completion gate:** History has a real source and observation window; schedule assumptions and collection gaps stay visible. Handoffs replay offline without implicit writes, and linear output works without a TTY.

### 6. Routine engineering workflows

Deliver concrete access explanation and capacity/autoscaling evidence, then storage expansion and node maintenance; manage local sessions before composing optional network probes/path review.

**Completion gate:** Per-workflow evidence and limitations are readable; maintenance/storage writes use stage 3 safeguards. Network configuration/policy candidates never masquerade as proven traffic or authorization.

### 7. Demand-gated optional integrations

Validate demand for fleet, upgrade readiness, security/policy, backup/restore, cost, historical observability, operator adapters and dependency review. Select providers from demonstrated tasks.

**Completion gate:** A concrete user scenario, supported provider and bounded scope are documented before implementation commitment. Unselected integrations remain an explicit optional backlog.

### Dependencies and delivery practice

- #76 resource-health truth precedes #78 dashboard redesign; #74 current drain validation precedes #58's larger maintenance workflow.
- #22/#42 provide shared action/layout rules for later screens; #45 selection safety precedes recovery execution in #51.
- #47 is required before execution in #51/#53/#57/#58 and mutation/session adapters; #63 provides contracts used by rendered/Git sources and optional providers. Read-only work can use existing foundations while shared contracts mature.
- #53 reviewed change sets consume #50 previews, #51 outcomes and #52 ownership; #54 adds configuration evidence.
- #55 joins #48 observed deltas, #51 release, #52 reconciliation and #60 Job evidence. #64/#80 share retained evidence models.
- #59 capacity supports #57 storage/#58 maintenance; #62 session lifecycle supports #61's optional probes. #56 access remains independently useful.
- Optional #72 dependency review builds on #61 and, only when configured, #65 fleet/#70 history. Optional providers are not prerequisites for ordinary native workflows.

Use focused PRs around one workflow or shared contract. Start from current master/applicable PR head, verify the existing implementation, and avoid recreating shipped foundations. Close a delivery issue only when its acceptance criteria are met, with a completion note linking the implementation and actual validation. Retain the operator-study gates on #22/#23. Update this tracker after each delivery; close #8 when all selected work is resolved and optional decisions are explicitly recorded.

The 8 optional issues (#65–#72) are demand-validation decisions, not a promise to implement all integrations. Of the 41 delivery issues, **33 cover fixes/workflows and 8 cover optional integrations**. Ticket counts do not measure effort.

## Issue inventory

### Phase 1: Reliability and session recovery

- [#73](https://github.com/jaredpricedev/k9s/issues/73) — [P1] Restore terminal and app resources on external signals
- [#74](https://github.com/jaredpricedev/k9s/issues/74) — [P1] Validate the visible drain form before submitting maintenance
- [#75](https://github.com/jaredpricedev/k9s/issues/75) — [P1] Keep command typing responsive during namespace discovery
- [#76](https://github.com/jaredpricedev/k9s/issues/76) — [P1] Show per-kind Pulse availability and isolate collection failures
- [#49](https://github.com/jaredpricedev/k9s/issues/49) — [P2] TK05: Refresh running session clients while retaining the workspace

### Phase 2: Complete the UI/UX foundation

- [#22](https://github.com/jaredpricedev/k9s/issues/22) — [P1] Keep contextual help and primary actions readable and accurate
- [#42](https://github.com/jaredpricedev/k9s/issues/42) — [P1] Define narrow and minimum layouts for workspace, investigation and review
- [#45](https://github.com/jaredpricedev/k9s/issues/45) — [P2] Keep rollout revision selection visible and explicit after refresh
- [#23](https://github.com/jaredpricedev/k9s/issues/23) — [P2] Make retained investigation summaries actionable and validate the operator flow
- [#43](https://github.com/jaredpricedev/k9s/issues/43) — [P2] Show the active workspace tab and prioritize collection gaps
- [#44](https://github.com/jaredpricedev/k9s/issues/44) — [P2] Put changed fields before repeated provenance in manifest review
- [#77](https://github.com/jaredpricedev/k9s/issues/77) — [P2] Prioritize stream coverage, loss and actions within narrow status strips
- [#78](https://github.com/jaredpricedev/k9s/issues/78) — [P2] Provide a readable narrow Pulse layout and reduce nested chart chrome
- [#79](https://github.com/jaredpricedev/k9s/issues/79) — [P2] Make error dialogs concise and directly recoverable
- [#46](https://github.com/jaredpricedev/k9s/issues/46) — [P2] Honor NO_COLOR and keep redirected informational output plain

### Phase 3: Shared execution and provider infrastructure

- [#47](https://github.com/jaredpricedev/k9s/issues/47) — [P1] OPS: Extend guarded operation lifecycle to inherited maintenance and provider actions
- [#63](https://github.com/jaredpricedev/k9s/issues/63) — [P2] TK19: Add typed on-demand tool and provider discovery

### Phase 4: Finish Change & Release

- [#50](https://github.com/jaredpricedev/k9s/issues/50) — [P2] TK06: Complete desired-state source providers and explicit server preview
- [#51](https://github.com/jaredpricedev/k9s/issues/51) — [P2] TK07: Complete rollout outcome tracking and guarded recovery workflows
- [#52](https://github.com/jaredpricedev/k9s/issues/52) — [P2] TK08: Add GitOps ownership chains and reconciliation progress
- [#54](https://github.com/jaredpricedev/k9s/issues/54) — [P2] TK10: Add configuration reference and rollout-impact review
- [#53](https://github.com/jaredpricedev/k9s/issues/53) — [P2] TK09: Add reviewed change sets with per-target progress and receipts

### Phase 5: Daily activity and handoffs

- [#60](https://github.com/jaredpricedev/k9s/issues/60) — [P2] TK16: Review scheduled and one-off job outcomes
- [#48](https://github.com/jaredpricedev/k9s/issues/48) — [P2] TK03: Add observed daily queue changes and scheduled-run history
- [#55](https://github.com/jaredpricedev/k9s/issues/55) — [P2] TK11: Build a retained application activity timeline
- [#64](https://github.com/jaredpricedev/k9s/issues/64) — [P2] TK20: Add repeatable read-only runbooks and task handoffs
- [#80](https://github.com/jaredpricedev/k9s/issues/80) — [P2] Provide linear read-only task output for accessibility and scripting

### Phase 6: Routine engineering workflows

- [#56](https://github.com/jaredpricedev/k9s/issues/56) — [P2] TK12: Explain a concrete Kubernetes access decision
- [#59](https://github.com/jaredpricedev/k9s/issues/59) — [P2] TK15: Compose capacity and autoscaling evidence into a review
- [#57](https://github.com/jaredpricedev/k9s/issues/57) — [P2] TK13: Add storage diagnosis and reviewed PVC expansion
- [#58](https://github.com/jaredpricedev/k9s/issues/58) — [P2] TK14: Add node maintenance planning and drain outcome review
- [#62](https://github.com/jaredpricedev/k9s/issues/62) — [P2] TK18: Manage local port-forward, shell and diagnostic sessions
- [#61](https://github.com/jaredpricedev/k9s/issues/61) — [P2] TK17: Add network path review with optional observed flow evidence

### Phase 7: Demand-gated optional integrations

- [#65](https://github.com/jaredpricedev/k9s/issues/65) — [P3] TK21: Validate demand and design bounded fleet workspaces
- [#66](https://github.com/jaredpricedev/k9s/issues/66) — [P3] TK22: Validate and add upgrade readiness evidence
- [#67](https://github.com/jaredpricedev/k9s/issues/67) — [P3] TK23: Validate security and policy review adapters
- [#68](https://github.com/jaredpricedev/k9s/issues/68) — [P3] TK24: Validate backup and restore review providers
- [#69](https://github.com/jaredpricedev/k9s/issues/69) — [P3] TK25: Validate cost and waste review with named pricing sources
- [#70](https://github.com/jaredpricedev/k9s/issues/70) — [P3] TK26: Validate historical observability provider integration
- [#71](https://github.com/jaredpricedev/k9s/issues/71) — [P3] TK27: Validate semantic operator workflow adapters
- [#72](https://github.com/jaredpricedev/k9s/issues/72) — [P3] TK28: Validate application dependency review from explicit evidence
