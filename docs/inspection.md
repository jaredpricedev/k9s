<!-- Modified for k9+; see NOTICE. -->

# Resource investigation

Select an API resource and run `:troubleshoot`. The snapshot places phase and
failure reasons, important conditions, container restarts and last termination,
and recent UID-scoped events before owner references. Full reported messages are
retained; `m` toggles compact messages and expands them from the captured evidence
without another API read. `/` searches the snapshot using a case-insensitive regular expression;
`-f text` uses fuzzy search. `n` and `N` move between matches. Invalid regex input
reports an error while keeping the evidence.

Every snapshot records its observation time and Kubernetes API source. Events
are limited to 100 retained entries and sorted latest first within the response;
they are not a complete history. Missing conditions, missing UID and unavailable
or absent events are visibility limits, not proof of health. Workload Pod reads
are selector-scoped and limited to 100 matches. Secret bodies and raw workload
specifications are excluded from this summary.

`g` opens related resources and Enter follows a reference. Esc returns one layer
to the retained snapshot, including its search and scroll offset. `r` explicitly
makes a new observation. A failed refresh keeps the earlier snapshot beneath the
failure notice. A known source UID is checked on later reads: a replacement with
the same name receives an identity-change notice. If the selected row has no
known UID, the first snapshot states that continuity with that row is unknown,
then retains the observed UID for subsequent refreshes and relationship reads.

Combined Flux rows resolve to the same native GVR, namespace and name as their
kind-specific list. Restricted/unavailable rows, Pulse and Xray provide an
explicit unavailable reason for API inspection. Their navigation actions remain
available through `:actions`.

A proposed three-transition common-failure workflow is a usability goal, not a
measured guarantee. Operator evaluation remains necessary; fixture-based tests
verify evidence order and retained navigation, not production diagnosis time.
