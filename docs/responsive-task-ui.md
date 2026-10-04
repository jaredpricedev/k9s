# Responsive task UI

The workspace, retained investigation, change-review and Pulse views share a minimum **40×12 available view**. Compact application chrome therefore needs a **40×16 terminal**. Below the minimum, task views show the required/current dimensions and keep Back/Quit available without replacing their retained state. Modal forms independently require a 40×16 terminal and block field/button input while their controls are invisible.

At 120×34, resource tables retain useful secondary fields. At 80×24, each task uses one primary pane, concise metadata and complete primary hints. At 60 columns, workspace tables keep identity and fault/status columns; exact kind, namespace, scope, full messages and capture time remain available with `v` (or Enter on Coverage). Native provider tables reserve status before optional revision/source/date columns. A date is either complete or hidden, never displayed as a partial field.

Workspace tabs mark the active tab in text, use 1–5 and Tab, and retain each tab's query/selection. Coverage lists gaps before completed reads using stable kind/namespace identity. Refreshing a reason does not move the selected source. A quiet partial queue remains explicitly partial.

Investigation Overview renders each current container state once. Previous termination is explicitly historical; proposed checks do not establish a cause. Narrow container tables move previous exits to separate labelled rows rather than wrapping a record. At the minimum, the identity bar contracts to one row and the summary preserves fault, readiness/restarts, previous exit, unknown metrics and the next evidence control. Exact UID/times/source limits and full messages remain in Evidence. Changing presentation does not recapture, rewrite or export less evidence.

Help is one scrollable key/action list at every width. Long availability reasons continue underneath their key, instead of pushing descriptions off screen. The application snapshot is overlaid by owned view/mode bindings. Compact hints use stable shortcut/action priority metadata, including hidden shared filtering bindings; wording changes do not remove a primary action. Local workbench help retains its existing routing. A/B comparison begins with “A captured; r capture B” after a successful A capture; actual denied/failed captures retain their error distinctions.

Forms keep the native tview Form fields, callbacks, focus and keyboard behavior. Labels stack below 80 columns. The focused field scrolls into view and Cancel/Save remain visible. PgUp/PgDn expose long guidance or validation details. Operational errors show the literal cause and recovery instruction without cow art. Callers can supply an explicit recovery action; failed command dialogs restore the exact command for editing and never retry automatically.

## Verification

Native tcell frame/state checks exercise 120×34, 80×24, 60×24, 40×16 forms and 40×12 investigation content, plus one size below the minimum. Checks cover long/CJK/combining values, monochrome/no-icons meaning, available and unavailable actions, stable coverage selection, editable command recovery, retained investigation query/scroll/evidence, provider status/date budgets, and staged A/B semantics. Existing safe collection/stream/export tests remain in the combined suite.

Captures and implementation notes: `/workspace/artifacts/k9plus-responsive-ui-2026-10-04/`.

Representative operator studies required by issues #22 and #23 are **not measured by these automated checks**. Those issues remain open for actual two-gesture discovery and three-transition investigation studies. A bounded real-PTY resize/exit smoke path should be run against the merged application; native frames are layout evidence, not proof of terminal lifecycle or human usability.
