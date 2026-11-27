# The corpus: pluxy's replies against CLIProxyAPI's

pluxy is meant to replace CLIProxyAPI without the coding tools pointed at it noticing the change.
The differential test corpus in `next/corpus` checks that: it sends pluxy the same requests that
CLIProxyAPI was once sent, and compares what pluxy does with what CLIProxyAPI did. This page
explains what the corpus compares and the bar it sets. How to run it and how to add a case is in
[`next/corpus/README.md`](../next/corpus/README.md).

## What a case compares

A case sends one client request (or a short series of them) to pluxy, whose provider account points
at a fake upstream server. The fake upstream answers with replies scripted in the case and records
every request it gets. The corpus then compares two things with CLIProxyAPI's recording of the same
case:

- the requests that reached the upstream, in the fields and order that matter;
- the reply the client got: status, headers, body, stream events and errors.

The cases are grouped by client endpoint (OpenAI Chat Completions; OpenAI Responses over HTTP and
WebSocket, and its compaction endpoint; Anthropic Messages; Gemini GenerateContent and
Interactions; image generation; the Codex CLI's search endpoint; model lists), each against the
providers that serve it. A few cover management API calls: account quotas, account model lists and
the import of a Vertex service account.

## The bar

pluxy must do what CLIProxyAPI did on every case, except where the case lists an exception with
its reason:

- `intended` when pluxy differs on purpose, such as passing a request to the upstream unchanged
  where CLIProxyAPI rewrote it, or a safer default (CORS headers only for listed origins, where
  CLIProxyAPI allowed every origin);
- `gap` when pluxy does not match yet and the difference is still to be settled.

An exception can also pin pluxy's own value, so it records pluxy's behaviour as well as excusing
the difference. The runner keeps the list honest in two ways: an exception that covers no
difference fails its case, and the test changes each value of pluxy's reply in turn and fails if
the case still passes, so an exception cannot hide a difference it was not written for.

The bar covers what pluxy keeps. A CLIProxyAPI setting or panel feature that pluxy leaves out is
listed with its reason: settings in
[What each setting does here](../next/README.md#what-each-setting-does-here), and panel features in
[What each page leaves out, and why](../next/ui/README.md#what-each-page-leaves-out-and-why).

## The recorded replies

Each case file holds CLIProxyAPI's reply, recorded from live runs of the copy of CLIProxyAPI that
this repository held until pluxy replaced it (in the case files, the field `recorded_old_core`).
That copy is no longer in the repository, so the corpus neither builds nor runs CLIProxyAPI: it
compares pluxy with the recordings only. That makes it fast enough for every CI run; `next-ci.yml`
runs it with the rest of the Go tests.

The recordings cannot be made again, so they are never edited or regenerated: they are the lasting
regression check. A new case reuses the recording of an existing case whose request and scripted
upstream replies are the same. A behaviour CLIProxyAPI never had has nothing to compare with, so it
is pinned in a Go test beside the code instead. The steps are in
[Adding cases](../next/corpus/README.md#adding-cases).
