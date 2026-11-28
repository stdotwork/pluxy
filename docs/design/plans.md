# Plans

This page lists the confirmed plans for pluxy that are not built yet, in the order they come. Why
pluxy is built the way it is, and what is built already, is in [design.md](design.md). Nothing here
has a date.

## The first goal

pluxy is to be something a person can sit down with, use every day from start to end, and polish:
every flow worked through until it feels natural, not only until it works. The first real test is
replacing a CLIProxyAPI that serves someone's daily coding sessions, with a tested way back to
CLIProxyAPI.

Until then the work stays on the main things: four providers supported fully, routing that can be
trusted, and a setup that mostly detects what is on the machine. New extensions and a website wait.

## What comes first

### Four providers supported fully

Claude, Codex, Gemini and OpenAI-compatible APIs are to be supported fully: every feature a coding
tool uses with them works through pluxy, including when pluxy translates between formats. Still
to do:

- **The remaining translation gaps** that hit these four. The list of gaps found so far has not yet
  been split by provider, so the size of this work is known only once that is done.
- **Web search when the upstream is Gemini.** Today the Gemini provider sends a client's web search
  as Gemini's search tool only when the account talks to Google's own API, the model supports the
  tool, and the request has no function tools beside it (Gemini's API does not take both in one
  request). Coding tools nearly always send function tools, so for them the search tool is
  dropped, and the drop is reported.
- **Video endpoints.** Image generation, speech and transcription endpoints exist; no provider
  serves video through pluxy yet.
- **A live quota reading for Gemini accounts.** Today a Gemini account's quota shows only what its
  last refusal for being over its limit said.

Kimi, xAI, Meta and Devin stay installable as they are, with no new work on them until the four
above are done.

### Routing

- **Accounts the provider closed come back on their own.** Today such an account stays out until a
  person signs in again or turns it back on. It is to be checked again on a schedule and brought
  back, and the person told when an account is taken out or comes back.

The rest of the confirmed routing work is built: timeouts for silent upstreams, reset and cooldown
handling, fallback chains a person builds, preferring the account whose quota resets soonest,
session affinity on by default, and replies that say which account served them.

### Tests

- **Tests for the known kinds of bug** reported against CLIProxyAPI that touch what pluxy ships and
  that reading the code could not settle, so each is shown fixed or found.

## After that

### Packages and the registry

- **A registry in four tiers:** hidden dependencies that other packages pull in, the setup picks
  (about ten), a verified marketplace, and unverified third-party sources installed only after a
  warning. Today a registry marks each package `pick`, `verified` or `unverified`, and core shows
  the mark and enforces nothing by it. Installing a package is to be as simple as dropping in a file, from an open-source
  community marketplace, an official marketplace whose packages may be closed-source or paid, or
  any other source, such as a GitHub repository, that pluxy does not verify.
- **A hosted, signed official registry**, so extensions and model lists update without a new
  pluxy.
- **More kinds of extension:** quota readers (see the quota-only extension below), request
  filters and event listeners (see [The extension model](design.md#the-extension-model)), and
  packages that carry themes.

### Usage and quota

- The cost of each request, by key and by account.
- A quota-only extension: connect an account only to read its quota, so every account's quota is
  in one place, including accounts used directly in the provider's own apps.
- Quota for API-key accounts beyond the vendors pluxy reads today (OpenRouter and DeepSeek), and
  quota in the desktop app's tray menu.
- More detail per request: time to first token, tokens per second, the reasoning effort used and
  the service tier.
- Detecting a model that the upstream degraded or swapped.

### Client keys and routing

- Client keys with limits: on or off, the models a key may use, a budget with a reset period,
  requests per minute and requests at once.
- Account groups, and keys bound to chosen accounts or groups.
- Spreading a session's subagents over several accounts.

### Security

- Masking secrets in prompts before a request leaves the machine: off by default, and a request is
  refused when masking cannot run.
- Credentials encrypted at rest.

### Extensions people asked for

- Scripted hooks on requests and replies, and webhooks.
- Prometheus metrics and token-saving compression, as marketplace extensions.

### Providers

Z.ai's GLM coding plans, GitHub Copilot, a Gemini CLI sign-in, Kiro, Cursor, keyless local models
such as Ollama's, Trae, Perplexity, Qwen, iFlow, CodeBuddy, Cline Pass and OpenCode Go, and full
support for Kimi, xAI, Meta and Devin.

### Coding tools and the app

- Setting up more coding tools: Cursor, Cline, Roo, Kilo, Droid and OpenClaw.
- Integrations set aside earlier: the agents Kimi, OpenClaw, ZCode, DeepSeek Harness, WorkBuddy,
  Antigravity CLI, Copilot CLI and VS Code's Copilot, and usage readers for Copilot, Gemini CLI,
  Antigravity, Kimi and ZCode.
- UI languages other than English, Chinese first.
- Deciding which remaining EasyCLIProxyAPI features to keep.
- Core's management key taken from the environment, for containers. (Today only the `pluxy`
  command reads a management key from the environment, and `PLUXY_HOME` names pluxy's folder.)

### Reaching pluxy

- An MCP endpoint for agents, with scoped tokens, as another client of the management API.
- Built-in Tailscale control, so a core on another machine can be reached without opening it to the
  internet.

### Releases

Installers, a licence, signed builds and a release channel come last.

## Open

- **How far extensions go.** The design leans towards widening extensions to the breadth of Pi's
  (hooks on requests, replies and streams; routing extensions that look like a model; extension
  commands and pages) and to Pi's way of making one (install from a git address or a folder, try
  one for a single run, script extensions that need no Go build). This is not decided.
- **An issue-triage command** like T3 Code's, which gathers what an agent needs to look into a
  problem and hands it to the person's own coding agent, is under consideration.

## Not planned

- Extensions that make other clients pose as Claude Code, or that rewrite words in a request to get
  past a provider's content filter, are never shipped or verified by pluxy. Others may build them.
- OmniRoute's routing strategies and LiteLLM's single sign-on.
