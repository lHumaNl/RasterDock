// Package control implements protocol v1's physical-key parser and synchronous
// receiver control gate. All calls belong to one serialized owner; no goroutines,
// platform input, UI, storage, or logging are performed here. Errors are returned
// to the owner for diagnostics. All timestamps must share a trustworthy local
// monotonic clock domain; synthetic clocks are supported for deterministic tests.
//
// Viewer integration: keep one Parser for each live viewer epoch, map physical
// digit keys to canonical "0".."9", and call Key with the prepared packet count.
// Apply accepted commands atomically to page ordinal, nonce, and frame publication.
// ResetInput on focus loss or disarm, not the entire Parser. A maximum nonce ends
// control for that epoch; reopen the viewer rather than clearing replay state.
//
// Receiver integration:
//  1. Validate the beacon using protocol/v1 and pin the persisted descriptor.
//     Establish capture timestamp provenance and explicitly Bind a current snapshot.
//  2. Feed observations via Observe, independently of packet persistence. A true
//     result acknowledges only the command. Propagate any error as a control stop.
//     Check(actualNow) after capture/decode and before Observe; a new capture must
//     not revive an expired binding. FreshUntil is read-only scheduling metadata,
//     not input authorization. The exact freshness boundary remains eligible.
//  3. Once storage/verified missing-plan logic explicitly permits a page request,
//     call Begin(ordinal, true, now, nativeChecker). The checker verifies current
//     adapter availability, foreground target, and operator authorization.
//  4. Send the returned command synchronously as physical F8, decimal nonce, Tab,
//     decimal ordinal, Enter. Call Sent at successful Enter completion; on any
//     partial/failed send call Stop. The same serialized owner may Observe during
//     the send to refresh freshness; no concurrent mutations are allowed. Matching
//     echoes cannot acknowledge while awaiting Sent and are never replayed. After
//     Sent, acknowledgment needs a newer frame captured after the first Enter.
//  5. Continue heartbeat observations, Check, and Retry polling. Retry's ready
//     result reserves one identical send; complete it using the same Sent/Stop
//     contract. Pending exposes deadlines for scheduling, not permission to send.
//
// Stop on operator revocation or adapter/source loss immediately. Observe never
// implicitly rebinds or resumes. Bind's cutoff excludes queued prebind captures;
// the bound snapshot is the sole exception and sets the initial highwater.
package control
