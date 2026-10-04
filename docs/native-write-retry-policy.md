# Native write response retry policy

Native clients built from `client.Config.RESTConfig` retain the first POST,
PATCH, PUT or DELETE response. The private config appends a transport wrapper
after existing caller wrappers. It clones a write response and its headers to
remove only `Retry-After`; status, body, resource identity and other headers
remain available to the caller. GET retries keep the server's original hint.
The supplied config and an inner transport's response/header map remain unchanged.

This addresses client-go v0.35.3's response retry path: its REST requests default
to ten retries and accept an integer `Retry-After` on HTTP 429 or any status at
least 500, including for writes. Its transient transport-error retry predicate
already applies only to GET. A server error after an attempted write can still
mean an unknown outcome; suppressing retries does not prove that a mutation was
rejected or committed. The operation owner must retain that uncertainty.
An unexpected proxy response after an attempted write is UNKNOWN, including
an unrecognized HTTP 429 or 409. Only a recognized optimistic-concurrency
conflict is eligible for the existing guarded conflict retry. A structured
API throttling rejection remains FAILED; any reported status at least 500 is
UNKNOWN after submission.

The shared config covers fresh typed and dynamic APIClient handles, committed
session clients built by `NewSessionConnection`, pinned native handles, and
native actors copied from their REST config. It therefore applies to server
previews/apply, rollout recovery, Job rerun, GitOps mutations, DAO mutations,
and owned debug-Pod creation/deletion. It performs no reads or writes itself.

This policy controls the SDK's response-driven retry hint. It cannot govern
retries hidden inside an opaque caller transport, external CLI/plugin tools,
or arbitrary third-party clients constructed without the native config path.
Existing operation destination/identity checks and response classification remain
necessary. Authentication and transport wrappers retain their normal behavior.

Actual HTTP fixtures exercise POST/PATCH/PUT/DELETE against HTTP 429 and 503
through freshly loaded config, prepared config, and committed sessions, using
both typed and dynamic clients. They verify one server contact, preserved API
status/identity/body, wrapper order, untouched original response headers, and
GET retry controls. These fixtures are not live-provider validation.
Actual typed/dynamic HTTP fixtures also pass errors through the operation
ledger and real conflict-retry helper, proving proxy uncertainty is retained
without another write. Existing recognized conflict retry checks still pass.
