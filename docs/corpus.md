# The corpus: how the new core is held to the old one

The core in `next/` replaces the old core, the Go proxy that was in `core/` until it was removed
from the repository. Users of the old core should be able to move without their clients noticing,
so the new core is checked against what the old one did on the same requests. The check is the
differential test corpus in `next/corpus`. This page explains the bar it sets and how the old core
it compares with is fixed. How to run it, write cases and re-record is in
[`next/corpus/README.md`](../next/corpus/README.md).

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

Each case file holds the old core's observations, recorded from a live run of it
(`recorded_old_core`). The corpus compares the new core with those recordings and does not build
or run the old core, so it is fast enough for every CI run; Next CI runs it with the rest of the
tests. The old core is no longer in the repository and nothing in CI runs it live, so the
recordings are the lasting regression check.

"The old core" in the corpus means the code that was in `core/` at one pinned commit, the last
commit that changed it; [`next/corpus/README.md`](../next/corpus/README.md) names that commit.
Re-recording a case, or comparing with the old core live, builds the old core from a worktree of
that commit and points the corpus at the binary, so cases can still be added and re-recorded.
The steps are in
[Re-recording against the old core](../next/corpus/README.md#re-recording-against-the-old-core).
