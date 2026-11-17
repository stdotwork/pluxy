# The corpus: how the new core is held to the old one

The core in `next/` replaces the old core (the Go proxy formerly in `core/`). Users of the old
core should be able to move without their clients noticing, so the new core is checked against
the old one on the same requests. The check is the differential test corpus in `next/corpus`.
This page explains the bar it sets and how the old core it compares with is fixed. How to run it,
write cases and re-record is in [`next/corpus/README.md`](../next/corpus/README.md).

## The bar

A corpus case sends one client request to a core whose provider points at a fake upstream, and
records two things: the requests that reached the upstream and the reply the client got. For
every provider the new core serves, it must do what the old core did on each case: the same
upstream requests (in the fields and order that matter), and the same replies, streams and
errors.

Where the new core differs, the case lists an exception with its reason:

- `intended` when the difference is on purpose, such as a request passed to the upstream
  unchanged where the old core rewrote it, or a safer default (CORS headers only for listed
  origins, where the old core allowed every origin);
- `gap` when the new core does not match yet and the difference is still to be settled.

An exception can also pin the new core's value, so it records the new behaviour as well as
excusing the old. The runner keeps the list honest: an exception that covers no difference fails
its case, and the test corrupts each value of the new core's reply in turn and fails if a case
still passes, so an exception cannot hide a difference it was not written for.

The bar covers behaviour the new core keeps. An old feature or setting is left out only with a
written reason: config settings in
[What each setting does here](../next/README.md#what-each-setting-does-here), and panel features in [What each page leaves out, and why](../next/ui/README.md#what-each-page-leaves-out-and-why).

## Recorded replies and the pinned old core

Each case file holds the old core's observations, recorded from a live run (`recorded_old_core`).
The everyday run compares the new core with those recordings and does not build or run the old
core, so it is fast enough for every CI run; Next CI runs it with the rest of the tests. While
`core/` is in the repository, the Next corpus workflow also runs the old core live, which catches
a recording that no longer matches it.

The recordings, not a live old core, are the lasting regression check. Removing `core/` ends the
live run in CI, so the old core is pinned to one commit: once `core/` is removed, "the old core"
in the corpus means `core/` at the last commit that still has it, and
[`next/corpus/README.md`](../next/corpus/README.md) names that commit from then on. Re-recording builds the old
core from a worktree of that commit and points the corpus at the binary, so cases can still be
added or re-recorded after `core/` is gone. The steps are in
[Re-recording against the old core](../next/corpus/README.md#re-recording-against-the-old-core).
