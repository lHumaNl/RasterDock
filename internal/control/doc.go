// Package control implements protocol v1's physical-key parser and synchronous
// receiver control gate. All calls belong to one serialized owner; no goroutines,
// platform input, UI, storage, or logging are performed here. Errors are returned
// to the owner for diagnostics. All timestamps must share a trustworthy local
// monotonic clock domain; synthetic clocks are supported for deterministic tests.
//
// Viewer integration: keep one Parser for each live viewer epoch, map physical
// digit keys to canonical "0".."9", and call Key with the prepared packet count.
// Apply accepted commands atomically to page ordinal, nonce, and frame publication.
// ResetInput on explicit cancellation/disarm, not on sender OS-focus changes.
// Never clear the entire Parser's replay state. A maximum nonce ends
// control for that epoch; reopen the viewer rather than clearing replay state.
//
// Receiver integration:
//  1. Validate the beacon using protocol/v1 and pin the persisted descriptor.
//     Establish capture timestamp provenance and explicitly Bind a current snapshot.
//  2. Feed packet-bearing observations via ObserveFrame. Only Admitted results
//     may reach packet persistence; Acknowledged confirms only the command.
//     The Observe compatibility wrapper returns ACK only, never packet admission.
//     Propagate any error as a control stop.
//     Incoming acquisition timestamps must be bounded and fresh at arrival.
//     Static pages do not expire authorization. FreshUntil is diagnostic only.
//  3. Once storage/verified missing-plan logic explicitly permits a page request,
//     call Begin(ordinal, true, now, nativeChecker). The checker verifies current
//     adapter availability, foreground target, and operator authorization.
//  4. Send the returned command synchronously as physical F8, decimal nonce, Tab,
//     decimal ordinal, Enter. Call Sent at successful Enter completion; on any
//     partial/failed send call Stop. Do not capture or Observe between keys.
//     Evaluate queued echoes after Sent; acquisition may precede Enter-up but
//     must follow the first reservation and exceed its pre-command highwater.
//  5. Continue Check and Retry polling even without new frames. Retry's ready
//     result reserves one identical send; complete it using the same Sent/Stop
//     contract. Pending exposes deadlines for scheduling, not permission to send.
//
// Stop on operator revocation or adapter/source loss immediately. Observe never
// implicitly rebinds or resumes. Bind's cutoff excludes queued prebind captures;
// the bound snapshot is the sole exception and sets the initial highwater.
package control
