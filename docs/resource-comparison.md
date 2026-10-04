<!-- Modified for k9+; see NOTICE. -->

# Compare two resource observations

Select an API resource and run `:compare`, or choose **Compare observations** in
the Actions palette. The first read captures **A, the chosen baseline**, with its
context, GVR, namespace, name, UID, source and observation time. **B starts as not
captured**. Press **r** when you want to capture B, then press it again whenever
you want another B. A stays fixed for the lifetime of the comparison view. Reopen
`:compare` from a resource to choose a new A.

The report shows added, removed and changed JSON-pointer paths. It compares API
observations; it does not imply that either object is desired configuration. A
same-context, same-resource, same-name object with another known UID is explicitly
labelled **RECREATED IDENTITY**. Unknown UIDs remain labelled unknown. If the object
was already replaced before A could be captured, A is stale and comparison is
unavailable. B deliberately records the current UID so replacements remain visible.

API bookkeeping is hidden initially. Press **n** to reveal it, then press **n**
again to normalize it. The only omitted paths are:

- `/metadata/resourceVersion`
- `/metadata/managedFields`
- `/metadata/selfLink`

The report lists omitted paths. Generation, timestamps, labels, spec and status
remain comparable. Lists are compared as complete values, preserving ordering;
there is no claim that a reordered list has the same meaning. Object map paths
are sorted to keep the report stable.

Permission denial, missing or unknown observations, stale selection and incomplete
inputs retain their own labels and reasons. They never become a “no differences”
result. A complete pair with no reported differences means only that its retained,
compared fields match. It establishes neither health nor observation continuity.

Snapshots keep at most 1 MiB of JSON, 10,000 fields, 32 nested levels and 64 KiB
per string. Larger or unsupported objects are marked incomplete and their content
is omitted. Comparisons retain at most 2,000 changes and display at most 256 KiB.
Truncation is explicit. Each API read times out after ten seconds, uses the pinned
original clients, and cannot update a closed view or a different context.

Secret bodies are excluded without an API GET. Sensitive field names, `data`,
`stringData`, last-applied manifests, credential-named environment values and
container command/argument values are redacted. Recognizable bearer tokens, JWTs,
AWS keys and private-key blocks receive the same text protections as log evidence.
Terminal controls are removed, and reported markup is displayed literally. These
protections are heuristic; review the sanitized content before copying or sharing.

Copy and save use the report's sanitized projection. For notes, selected snippets
and a portable versioned format, see [evidence bundles](evidence-bundles.md).

The comparison operates on one selected resource. Desired-source comparison,
server-side apply, arbitrary raw YAML and automatic selection of a new baseline
are outside this workflow.
