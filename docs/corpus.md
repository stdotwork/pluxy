# The corpus: how the new core is held to the old one

The core in `next/` replaces the old core, the Go proxy that was in `core/` until it was removed
from the repository. Users of the old core should be able to move without their clients noticing,
so the new core is checked against what the old one did on the same requests. The check is the
differential test corpus in `next/corpus`. This page explains the bar it sets and what it compares
the new core with. How to run it and add cases is in
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

## The recorded replies

Each case file holds the old core's observations, recorded from live runs of it before it was
removed (`recorded_old_core`). The corpus compares the new core with those recordings only: it
does not build or run the old core, so it is fast enough for every CI run, and Next CI runs it
with the rest of the tests. The old core is no longer in the repository and nothing builds or runs
it, so the recordings are never re-recorded or edited, and they are the lasting regression check.

No case can be recorded from the old core any more. A new case reuses the recording of an existing
case whose request and scripted upstream replies are the same, and a behaviour the old core never
had is pinned in a Go test beside the code instead. The steps are in
[Adding cases](../next/corpus/README.md#adding-cases).
