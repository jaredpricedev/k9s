<!-- Modified for k9+; see NOTICE. -->

# Portable investigation evidence, version 1

Select a resource and run `:evidence` to capture a bounded API observation and up
to 100 UID-scoped retained events. The preview names the context, GVR, namespace,
name, UID, source, observation time and completeness. It saves no file automatically.
Event text is capped at 8 KiB and explicitly reports truncation and visibility gaps.

From a resource comparison, `:evidence` includes the frozen baseline A and any
already captured B, preserving each original identity, source and capture time.
From an inspector it includes the last successful retained snapshot text, observed
UID and capture time, with explicit truncation at 8 KiB. The resource object is
marked incomplete because only inspection text was retained. These exports make
no new API requests, and a failed refresh cannot replace the retained evidence.

Use **n** to add notes or **l** to add an explicitly selected snippet with a named
source. A snippet's timestamp records selection time; supply original event timing
in its text when needed. The preview shows exactly the sanitized content included.
Use **s** and explicitly choose a new absolute `.json` or `.md` path. Export creates
a new file with mode `0600`; existing files are never overwritten. Review all fields
before sharing. Secret bodies, data, credential-shaped fields and resource command
arguments are excluded. Recognizable token/private-key text is redacted using the
logstream protections. This is heuristic redaction, not a universal guarantee.

Run `:evidence-open /absolute/file.json` (or `.md`) for offline inspection. Import
never contacts a recorded context or applies a resource. Terminal controls are removed
and the viewer escapes markup. Notes can be added and the resulting preview saved
to a different new file. Future raw capture or fleet features require a separate design.

The JSON format has `version: 1`, `created_at`, `observations`, optional `notes` and
`snippets`, and `limits`. Observations retain the same safe schema as resource comparison:
`identity`, `source`, `observedAt`, `state`, `reason`, `limits` and `object`. Markdown
wraps that same JSON in a `json k9plus-evidence-v1` fenced block, so both formats round-trip.
Unknown fields, unsupported versions, missing identity/source/time and malformed
documents are rejected. There is no apply operation in the format.

Limits are 2 MiB per bundle, eight observations, 64 notes of 16 KiB each, 20 snippets
of 8 KiB each and 32 completeness statements. Each object obeys the comparison
snapshot limits. Large/unavailable observations retain their explicit state and
reason; they never become a claimed empty or healthy snapshot. Multi-resource evidence
is not an atomic cluster snapshot or a complete history.

The terminal preview is capped at 256 KiB. Larger previews ask you to select less
evidence before saving, so the app does not export fields hidden by truncation.
