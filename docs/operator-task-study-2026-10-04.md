# Operator task study packet: action discovery and retained investigations

**Status: protocol prepared; no participants recruited and no study run.** This packet supports the remaining human acceptance criteria for [#22](https://github.com/jaredpricedev/k9s/issues/22) and [#23](https://github.com/jaredpricedev/k9s/issues/23). It contains no participant results. Do not close either issue from automated tests, terminal captures, or this protocol alone.

The study asks engineers to do ordinary morning triage in a disposable Kubernetes-like workspace. It compares the prior view with the current view using the same task goals and equivalent sanitized evidence. It is not a production troubleshooting or diagnosis study.

## Participant handout

Give each participant this section only. Do not show the facilitator notes or task interpretation key below it. Replace bracketed setup labels with neutral labels before the session; do not describe which version is expected to be easier.

### Introduction to read aloud

We are evaluating two versions of a Kubernetes workbench, not testing you. Please work as you normally would when checking an application at the start of a workday. You may stop, ask to pause, or skip any task. Please say what you are looking for as you work if that feels comfortable. I may stay quiet while you try something so I can see what the screen communicates on its own.

Everything here uses made-up names and disposable local data. There is no live cluster and no production system. Do not enter credentials or real customer information. Some controls may look familiar but this session does not ask you to perform a real change. Tell me before attempting any action that could change data.

### Tasks

Read each task exactly as written. Do not explain where to click, which key to press, what screen to open, or which answer is expected. If the participant asks for help, say: “Please do what you would normally do; I’ll note where the interface needs help.” If they ask what a displayed fact means, say: “What do you think it means?”

1. **Find an action.** The `checkout-api` workload is not becoming ready. Find a way to see what actions are available for this screen. Tell me which action you would take next and what you expect it to do. Do not start a change.
2. **Check a failing service.** The team says `investigation-api` is repeatedly restarting. Find the main thing happening now, identify the affected container, and decide what evidence you would check next. Tell me what you believe is current, what may be older evidence, and what is not known from this screen.
3. **Check a placement problem.** A new `worker` Pod is waiting to start. Find the best available explanation and say what you would check next. If a measurement or permission is missing, explain what that does and does not tell you.
4. **Review a rollout.** A teammate says the latest `checkout-api` rollout may have changed behavior. Find the available comparison or rollout evidence. Tell me what changed, what remains uncertain, and what you would inspect next. Do not apply or roll back a change.
5. **Follow a service connection.** Requests are not reaching `checkout-api`. Starting from the service, follow the available path toward the workload. Tell me which destination you reached, what relationship the screen shows, and whether that alone proves connectivity.

After each task, I will ask: “What do you think is happening?”, “What would you do next?”, and “Where would that action take you or what would it affect?” There is no preferred wording. Please describe what the interface led you to believe.

### Session close

Which screen, label, or action was hardest to understand? Was there anything you expected to find but could not? Is there anything you would change before using this for real work?

## Facilitator protocol

### Participants and comparison

The proposed sample is six to eight engineers with mixed Kubernetes experience, as stated in the toolkit roadmap. This is a small qualitative evaluation, not a representative survey. Record experience in the participant's own broad terms; do not infer skill from job title. Participation is voluntary. Obtain any consent required by the host organization before recording. Prefer written observations; if recording is approved, keep it local, omit names and credentials, and follow the organization's retention rules.

Use the same five task goals and equivalent sanitized fixture state in both versions. Randomize and balance version order using sealed assignment cards: for an even sample, use equal numbers of A→B and B→A cards; for an odd sample, randomize which order receives the extra card. Shuffle the cards and assign without replacement. Randomize task order independently for each participant using a shuffled task-card deck; record both orders. Keep task wording identical between versions. Take a short reset break and restore the fixture between versions. Do not tell participants that A is old or B is new; label versions neutrally (A/B), and do not claim either is the control.

Run both pinned versions against this fixture only if each binary starts against the generated loopback kubeconfig in read-only mode. The manual helper is the launch/setup mechanism for both versions; it does not prove that the old version supports every task affordance. If a prior version cannot safely run a task at the same dimensions or lacks an equivalent starting view, do not substitute screenshots and describe the result as a complete comparison. Record the limitation and run only comparable tasks. Do not run against a real cluster.

### Safe setup and evidence references

- Use [`scripts/operator-study-fixture.py`](../scripts/operator-study-fixture.py) for a long-lived manual loopback session. Unlike the auto-driving journey scripts, it keeps its API available until the participant exits the app. It reuses the sanitized `WorkspaceAPI`/handler from [`scripts/daily-workspace-journeys.py`](../scripts/daily-workspace-journeys.py), adds the named `worker`, `checkout-api` Deployment/Service/Pod, and reuses the `investigation-api` CrashLoop/previous-OOM record. The Service uses the standard Kubernetes `spec.type` and `spec.clusterIP` fields. No live cluster is read. The API binds to `127.0.0.1`, injects explicit synthetic HTTP 403 gaps for metrics and `ops` event reads, rejects resource mutation verbs with HTTP 405, and records a request journal. Its only accepted POST is a synthetic `SelfSubjectAccessReview`, required for native permission checks; ordinary resource-create POSTs are rejected. These 403s are fixture responses, not evidence about a live cluster's RBAC. The app runs with `--readonly` as a second guard.
- From the repository root, first check fixture syntax and behavior:

  ```sh
  python3 -m py_compile scripts/operator-study-fixture.py
  python3 scripts/operator-study-fixture.py --smoke
  ```

  For each participant/version, run a separate fresh fixture and unique journal path. Example:

  ```sh
  python3 scripts/operator-study-fixture.py --binary /absolute/path/to/k9plus-A --journal /private/session-dir/p01-A-requests.json -- --command 'pods apps'
  ```

  Replace `k9plus-A` with the exact pinned executable and the journal path with a new private path. Repeat for version B using the same task data and starting command. Do not reuse a journal path. The helper generates a private temporary kubeconfig and isolated app configuration, launches the actual interactive app, then shuts down and removes the temporary files on normal exit or Ctrl+C. It writes the journal with mode `0600`; retain/delete it under the consent and data-handling rules above. Confirm the printed mutation-attempt count is zero; if it is not, stop the session and preserve the journal for review. The API fixture can be smoke-checked for named objects, Unschedulable/current-CrashLoop/prior-OOM states, explicit denied reads, mutation rejection, and server shutdown with `--smoke`.
- The corresponding native auto-driving evidence is not an operator result. Existing references include [`scripts/regression-journeys.py`](../scripts/regression-journeys.py) and artifacts under `/workspace/artifacts/k9plus-daily-workspace/` and `/workspace/artifacts/k9plus-full-ui-ux-review-2026-10-04/`.
- Review the generated fixture inventory and existing artifacts under `/workspace/artifacts/k9plus-daily-workspace/` and `/workspace/artifacts/k9plus-full-ui-ux-review-2026-10-04/`. Confirm data contain only synthetic names and values before sharing a screen or recording.
- Pin and record the exact executable SHA, source revision, terminal emulator, terminal dimensions, color/icon settings, fixture revision, and version label for each run. Capture baseline and current versions at the same size, with the same settings. The roadmap's UI baseline is 80×24; also record 60×24 when safely supported because #22 explicitly names it. Do not infer results for untested sizes.
- Disconnect cloud credentials and ensure fixture endpoints bind only to loopback. Verify the session can make no writes to any non-fixture target. If that cannot be established, do not run the session.
- Reset state between tasks and versions. Use equivalent starting selections and identical sanitized records; document any mismatch rather than silently compensating.
- Keep a neutral observer log. Note the first visible screen, key presses/mouse actions, wrong turns, help requested, returns, scroll events/distance, task completion or stop, and destination/context shown before a consequential action. Do not coach during a task.

### Native setup smoke already completed

This records setup evidence only; no participant sessions have run. On 4 October
2026, the manual helper launched `/workspace/.k9s-dev/bin/change-set-ready`
against its loopback fixture with `--command 'pods apps'`, context `study-dev`,
and namespace `apps`. The interactive app initialized the selected Pod list,
showing the synthetic `investigation-api` CrashLoopBackOff and `checkout-api-0`;
`:q` exited successfully. The helper removed its temporary kubeconfig and app
directory. Its mode-0600 journal at
`/tmp/k9plus-operator-study-native-smoke-20261004c.json` recorded 38 requests,
including six synthetic SelfSubjectAccessReview requests and zero resource
mutation attempts. The binary was built from source revision
`d992a2fde65e7b4ca163212e28575235b7b4c539` with Go 1.25.8 and has SHA-256
`8e0c6695191cb21bf83364f1f94c6e833145b90c9e8a79682ce7bdbfffd67787`; the
fixture helper then had SHA-256
`5a11c4a3d4b2cd2bb496fe9ee65e989d4f8e3220c4ab5cf60d146479b6c42755`.
This older binary demonstrates that the manual setup path works. It is not the
final study candidate and does not establish usability or task success.

### Facilitator record: earlier native walkthrough and separate ACK test (historical)

The earlier 14-chapter, 98-second movie was captured from source
`826790070e3d9dab1deff6f4e6cde16a47262265`. Its MP4 is
`/workspace/artifacts/k9plus-toolkit-walkthrough-2026-10-04/final-candidate-82679007-navigation/toolkit-walkthrough.mp4`,
SHA-256 `414417ad86ee9e860c899ee62da5843cb8f3a026a10c82875b7a25ca8e109fb0`;
the captured binary SHA-256 is
`a3d792d460103f6f8688bd6e263e547ee72ec07b8c8df1283388ad34965b3181`. The
primary movie's separate synthetic-fixture request journal recorded 147
requests. This was guided fixture coverage, not an operator study or latency
measurement. The seven-check acknowledgment envtest journey is a different
artifact, also from source `82679007`; it made two explicit synthetic fixture
resource writes. Do not combine that journey's checks/writes with the movie's
147-request journal. Neither artifact records participant results, and this
earlier candidate is superseded by the current acceptance source below.

### Facilitator record: native UI acceptance coverage in the current source

Candidate commit `c0e5ff82f5f965a2e630a5435795bc21d470da91` has tree
`d9cff84708c0b845f3bd5fae9bde388dbad02156` and is based on master
`d55c090487a72cf09a89a687bae6d0c3c70765ec`. PR #121 is merged; the PR #122
candidate adds the wide-Coverage hint correction plus a native regression test.
The PR #121 acceptance manifest is
`/workspace/artifacts/k9plus-ui-acceptance-2026-10-04/manifest.json`; it records
15 Help/action/investigation tests and local checks on the earlier `f7b503f3`
tree. PR #122 adds the sixteenth acceptance test function,
`TestActionCatalogHintsReservePrimarySpaceForAvailableActions`, and native
80×24/120×34 Coverage assertions. Its proof packet is
`/workspace/artifacts/k9plus-primary-hints-2026-10-04/manifest.json`; it records
reproduction on `f7b503f3`, focused race (16.793 seconds), full view race
(70.268 seconds) and pinned golangci-lint 2.6.2 (zero issues) on the `d9cff847`
tree. This is source-tree evidence only.

Coverage includes Help listener stop/restart and keyboard-reachable long labels
and reasons; contextual workspace, desired-review, table, comparison, Pulse,
Hubble and log action routing/hints; Details debounce ownership and stale-result
rejection; and investigation first viewport, retained export after failed
refresh, typed search/scroll and draft acceptance, related navigation and Esc return. The exact test
names and limits are in the manifest. In particular, Pulse positive Enter to
the browser was not newly tested; the cross-namespace return case models an
already accepted target; and monochrome coverage does not include every missing
condition/event presentation. These are regression checks, not measures of
learnability or task success. Both #22 and #23 human studies remain unrun. The
prior `f7b503f3` candidate had a wide-terminal Coverage hint defect: at 120
columns it promoted disabled Pod logs as the primary action despite the
availability reason. The PR #122 source corrects the candidate and adds
`TestActionCatalogHintsReservePrimarySpaceForAvailableActions`, which checks
hint priority; the full native 120/80-column test also verifies that the actual
Coverage header omits disabled actions. Focused and full view race checks and
pinned lint passed locally on this same tree. Thus current-source tests support
the documented nonhuman #22/#23 criteria, but do not establish operator
learnability or task success.

| Acceptance area | Exact tests in the candidate tree |
| --- | --- |
| Help lifecycle and reachability | `TestHelpAcceptanceStoppedPageDoesNotReceiveSkinUpdatesAndRestarts`; `TestHelpAcceptanceLongLabelsReasonsAndSectionsAreKeyboardReachable` |
| Action dispatch and visible hints | `TestActionDispatchAcceptanceLogsLocalHelpAndCommandEditing`; `TestActionDispatchAcceptanceHubbleLocalHelpAndCommandEditing`; `TestActionDispatchAcceptanceWorkspaceKeysAndPrompt`; `TestActionDispatchAcceptanceDesiredReviewKeys`; `TestActionDispatchAcceptancePulseTabs`; `TestActionDispatchAcceptanceTableSpaceAndFilter`; `TestActionDispatchAcceptanceComparisonWaitsForAAndPaletteCapturesB`; `TestActionCatalogHintsReservePrimarySpaceForAvailableActions` |
| Debounced Details search ownership | `TestActionDispatchAcceptanceDetailsDebounceUsesCurrentDispatcherOwner`; `TestActionDispatchAcceptanceDetailsRejectsDelayedStaleText`; `TestActionDispatchAcceptanceDetailsRejectsDelayedPreviousOwner` |
| Investigation presentation and return | `TestInvestigationAcceptanceFirstViewportAcrossColorModes`; `TestInvestigationAcceptanceRetainedExportSurvivesPresentationAndFailedRefresh`; `TestInvestigationAcceptanceTypedRelatedEscRetainsTabSearchAndViewport` |

### Facilitator record: current native walkthrough provenance

The 14-chapter guided walkthrough uses candidate source
`c0e5ff82f5f965a2e630a5435795bc21d470da91` (tree
`d9cff84708c0b845f3bd5fae9bde388dbad02156`) and binary SHA-256
`2f9d0dba8ce863c38d5171ebdea25c8d0ef6d70b2df7b2f09e7cd4beb2143c64`. The MP4
is `/workspace/artifacts/k9plus-toolkit-walkthrough-2026-10-04/final-candidate-c0e5ff82-native/toolkit-walkthrough.mp4`,
SHA-256 `8d419a011ec5bacc39a455f5606c6a4b72b6bebe07361bd6b972e0932334e2af`
(1,097,610 bytes; H.264/yuv420p, 1236×846, 12 fps, 98 seconds). Its journal in
the same directory records 147 fixture API requests (141 GET, 6 authorization
review POST), zero resource mutation requests, and successful cleanup. The
strict Coverage-header assertion shows Retained evidence, Search, Refresh and
navigation controls, with no unavailable Pod logs or Investigate hint. All 14
native frames and the complete encoded video were reviewed. This is guided
synthetic-fixture coverage, not an operator study or latency measurement. PR
#122 is still open in this source snapshot.

### Acknowledgment discoverability sentinel

Record this separately from #22/#23 task success. The earlier 826 capture showed
an initially blank acknowledgment box. The visible-box plus Space correction is
in shared UI PR #119 and current master `d55c0904`. The seven-check ACK envtest
is a separate source-826 journey with two fixture resource writes; it is not part
of the current 147-request movie journal. Neither automated artifact measures
whether a participant notices the control. Until a participant study is run,
mark the human sentinel “not run”; do not ask participants to compensate for it.

When enabled, present the ordinary, safe confirmation flow at its natural point, with a harmless fixture-only destination. Do not point out the acknowledgment control. Record whether the participant notices it without prompting, how they identify it, whether keyboard focus and Space work, whether the current destination is understood, and whether they can safely cancel. Do not perform a real destructive operation. This is a discoverability observation, not evidence that #22 or #23 has passed.

### Facilitator-only task interpretation key

Keep this section out of the participant handout. These are evidence points to observe, not scripted correct answers; accept equivalent plain-language explanations and record uncertainty rather than correcting the participant.

| Task | Evidence to record | Interpretation prompts for debrief |
| --- | --- | --- |
| Find an action | How the participant discovers contextual help/action search; gestures before discovery; whether they can distinguish available from unavailable actions and explain a primary action without invoking it. | Can they identify a valid next action and its effect from the current screen? Did the action list preserve useful unavailability reasons? Did a key shown in help match the active view/mode? |
| Failing service | Whether the participant identifies the current CrashLoop finding and affected container; distinguishes prior OOM/termination and retained events from current cause; notices unavailable metrics/coverage; finds logs/events/pressure or another evidence action; returns to the same retained selection. | Did they treat previous OOM as proof of the current cause, or missing metrics as zero/healthy? What evidence did they choose and why? Did search, tab, scroll, selection, and return retain the same resource identity? |
| Placement problem | Whether they find FailedScheduling/placement evidence and say what it supports; distinguish absent metrics or denied access from a measured zero or healthy state; choose a relevant next check. | What did the participant believe the missing data meant? Did they make a claim beyond the displayed evidence? |
| Rollout review | Whether they locate the requested rollout/change evidence, explain source/revision and target destination, distinguish observed from proposed/unknown outcomes, and avoid treating preview as applied change. | Can they describe what was compared and what is not established? Did they understand the selected destination before a possible action? |
| Service connection | Path of navigation from Service through endpoints/workload/Pods; selected namespace/context and final destination; whether they distinguish configured/reported relationships from tested connectivity. | Did the journey reach the intended workload? What did the screen actually establish? Did they mistakenly infer a successful network probe? |

For #22, report observed action-discovery gestures and whether the operator reached a useful action within the original two-gesture goal; do not retroactively redefine the gestures. Also report presentation issues at 80×24 and 60×24, help reachability, binding agreement, primary hint discoverability, and guard/reason understanding. The issue's automation and native captures are implementation evidence, not substitutes for this participant evidence.

For #23, report observed transitions for the common-failure task and whether the original three-transition goal was reached; count only transitions the operator actually made and state the counting rule. Report current-versus-historical-versus-unknown interpretation, chosen next evidence action, return/retained-selection behavior, destination awareness, time, scrolling, and any errors. Do not claim diagnosis accuracy for a fixture as production incident accuracy.

The roadmap's ten-second fault/container recognition and thirty-second next-evidence-action values are provisional design targets only. Record elapsed times, but do not treat those values as pass/fail thresholds or claim that the targets are validated. Report distributions and individual task context; with six to eight participants, avoid significance or population-wide claims.

## Blank recording sheets

Copy one participant sheet per session. Use a participant code, not a name. Leave fields blank if not observed; never reconstruct a gesture or answer from memory as if measured.

### Session metadata

| Field | Record |
| --- | --- |
| Participant code / date | |
| Experience in participant's words | |
| Version order / task order | |
| Version A revision + executable SHA | |
| Version B revision + executable SHA | |
| Fixture helper revision + script SHA | |
| Terminal, dimensions, theme, icons | |
| Fixture revision / reset confirmation | |
| Recording consent and method | |
| Acknowledgment sentinel build verified? | |

### Task observations

| Task / version | Start and finish time | First action and discovery gestures | Help, wrong turns, returns | Scroll distance/events | What participant said was current / historical / unknown | Next action chosen and why | Destination/context awareness | Completed, stopped, or assisted? Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Find an action | | | | | | | | |
| Failing service | | | | | | | | |
| Placement problem | | | | | | | | |
| Rollout review | | | | | | | | |
| Service connection | | | | | | | | |

### Acknowledgment sentinel (separate observation)

| Visible before action? | Noticed without prompt? | How identified? | Keyboard focus / Space worked? | Destination understood? | Canceled safely? | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| | | | | | | |

### Participant debrief

| Question | Notes in participant's words |
| --- | --- |
| Hardest label, screen, or action | |
| Expected but missing | |
| What would they change? | |
| Confidence or uncertainty they volunteered | |

## Analysis template (complete only after sessions)

Do not fill this section with expected values. Preserve task/version order and report skipped, assisted, interrupted, or invalid-comparison tasks explicitly.

- Participants completed: `__/__` (planned range: 6–8)
- Fixture, executable revisions, and session deviations:
- Order allocation and actual task order:
- #22 action-discovery gestures by task/version (individual observations and range):
- #22 visible/help-reachable action and binding mismatches:
- #23 transitions by task/version and stated counting rule:
- Current / historical / unknown interpretation observations:
- Next-action choice and reasons:
- Destination/context awareness and any incorrect destination:
- Task times and scroll/return observations (descriptive only):
- Acknowledgment sentinel: build revision, number run, unprompted discovery observations; separate from issue results:
- Automated/native fixture evidence used as setup verification (revision and scope; not participant findings):
- Limitations, deviations, and unresolved questions:
- Follow-up changes suggested by observed participant behavior:

Only after sessions are actually run, attach sanitized notes to the appropriate issue and report measured outcomes, method, revision, sample size, deviations, and limitations. Keep raw recordings private under the applicable consent and retention rules. This packet itself is not evidence that the human acceptance criteria have been met.
