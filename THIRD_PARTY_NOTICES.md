# Third-party notices

This repository once held, or still holds, code from the upstream projects below. Their licence
notices are kept here, each with the full text of the licence it came with. Where `next/` keeps a
file from a project, or adapts one, its section names the file and says how close the copy is:
*copied* is the same text; *adapted* is the same logic, structure or constants, changed in places or
moved to another file or language; *follows* is the same rules or behaviour written afresh.

## router-for-me/Cli-Proxy-API-Management-Center

The web panel formerly in `extensions/panel/`, from
<https://github.com/router-for-me/Cli-Proxy-API-Management-Center>. It was removed when the web
panel in `next/ui` replaced it. `next/ui` keeps one thing from it: `niceCeil` and `axisMax` in
`next/ui/kit/trafficChart.tsx`, adapted from `src/features/dashboard/utils.ts` (the same step ladder
and rounding, with the comments rewritten). Licence text, as it was in
`extensions/panel/LICENSE`:

~~~text
MIT License

Copyright (c) 2026 Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
~~~

## router-for-me/CLIProxyAPI

The proxy core formerly in `core/`, from <https://github.com/router-for-me/CLIProxyAPI>. It was
removed when the new core in `next/` replaced it. `next/` keeps from it:

- copied: `next/internal/legacy/testdata/config.example.yaml` (from `config.example.yaml`) and
  `next/extensions/codex/client_models.json` (from `internal/registry/models/codex_client_models.json`),
  both byte for byte;
- adapted: the model lists of the antigravity, claude, codex, devin, gemini, kimi, meta and xai
  extensions, made from its model catalog in `internal/registry/` (`models.json` and
  `devin_models.json`);
- adapted: `next/internal/route/sanitize.go`, from `sdk/cliproxy/auth/selector.go`. Its 17 regular
  expressions are copied unchanged and the redaction steps, the list of error texts and the
  256-character cut follow the original step by step, split into two functions. Two patterns the
  original defines but never uses are left out;
- copied, into a new test function: the 63 cases of `next/internal/route/sanitize_test.go`, from
  `TestExtractUpstreamErrorSummary_SanitizationAndTruncation` in
  `sdk/api/handlers/handlers_error_response_test.go` and
  `TestExtractUpstreamErrorSummary_AuthPackageDirectSanitization` in
  `sdk/cliproxy/auth/conductor_selection_cooldown_test.go`;
- adapted: the Codex `apply_patch` parameter schema and instruction text in
  `next/extensions/xai/patch.go` are the constants of `internal/client/codex/apply-patch/tool.go`
  byte for byte, and the description it builds follows that file's `Description`;
- adapted: the table of old and v8 config key paths (`moves`) in `next/internal/legacy/config.go`,
  written out from `internal/config/config_v8.go`;
- follows: in `next/extensions/codex/request.go`, the raising of a small output budget to 16 follows
  `normalizeCodexOutputBudgetDoc` in `internal/runtime/executor/codex_executor_request.go`, and the
  prompt cache key from a Claude Code session and agent follows `ClaudeCodePromptCache` in
  `internal/runtime/executor/helps/claude_code_session.go`; both are written afresh, with no code copied;
- follows: the tool call pairing in `next/format/anthropicmessages/turns.go` (`pairToolCalls`,
  `pairRun`), from `repairClaudeToolPairing` in
  `internal/translator/claude/openai/responses/claude_openai-responses_request.go`: a tool call
  with no result gets an error result, a result that answers no call is sent as text, and results go
  first in their user turn. The error result's text is copied;
- the replies recorded in the cases of `next/corpus/` are what this core answered, and a few of
  them hold model lists from its catalog.

Licence text, as it was in `core/LICENSE`:

~~~text
MIT License

Copyright (c) 2025-2005.9 Luis Pater
Copyright (c) 2025.9-present Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
~~~

## router-for-me/EasyCLIProxyAPI

The desktop app in `extensions/app/`, from <https://github.com/router-for-me/EasyCLIProxyAPI>. It is
still in this repository, with its licence in `extensions/app/LICENSE`; when it is removed, this
section is its notice. `next/` keeps from it, all in Rust or TypeScript as the original:

- adapted: the session listing in `next/app-tools/codex-sessions/src/reader.rs`, from
  `src-tauri/src/codex_sessions.rs`: database discovery, the queries on the `threads` and
  `automation_runs` tables, merging, the rollout and `session_index.jsonl` listing and the thread ID
  rules, with names changed. Its message text readers (`content_text`, `reasoning_text`, `is_tool`) are
  adapted from `src-tauri/src/codex_sessions/context.rs`. The read-only opener, the size limits, the
  detail paging, export and open are new;
- adapted: four tests of `next/app-tools/codex-sessions/src/reader_tests.rs` (the ones whose names
  start `list_` or `listing_`), from the tests of `src-tauri/src/codex_sessions.rs`;
- adapted: `maskEmail`, `maskOpaque`, `maskIdentifier`, `identityVariants` and `escapeRegExp` in
  `next/ui/kit/privacy.ts`, from `src/services/privacyMask.ts`, with one more name form in
  `identityVariants` and a different URL credential pattern. The rest of the file is new;
- adapted: `counts`, `first`, `count`, `model_name`, `clean_model` and `timestamp` in
  `next/app-tools/lib/usage/src/fields.rs`, from `sample_from_map`, `first_counter`, `as_counter`,
  `string_field`, `sanitize_model_name` and `extract_local_timestamp` in `src-tauri/src/agent_usage.rs`:
  the same field names and rules, without the `chrono` dependency;
- adapted: `event` in `next/app-tools/usage-codex/src/rollout.rs`, from `extract_codex_usage` in
  `src-tauri/src/agent_usage.rs`.

Other app tools in `next/app-tools/` follow this app's rules for the files of the agents they set up
or read (for example `usage-claude`, `lib/pi-sessions` and `claude-desktop`): the rules are the
original's, the code is written afresh.

Licence text, as it is in `extensions/app/LICENSE`:

~~~text
MIT License

Copyright (c) 2026 Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
~~~

## router-for-me/pi-cliproxyapi-provider

The CLIProxyAPI provider extension for Pi, from
<https://github.com/router-for-me/pi-cliproxyapi-provider> (npm `@router-for-me/pi-cliproxyapi-provider`),
version 1.4.25. `next/app-tools/pi-setup/adapter/` holds a reviewed and patched copy of it, with its
own `LICENSE` and `package.json`, which `pluxy/pi-setup` writes into a Pi agent folder as a local Pi
package. The old desktop app installed the original from npm; this repository carries the copy
instead. Licence text, as it is in `next/app-tools/pi-setup/adapter/LICENSE`:

~~~text
MIT License

Copyright (c) 2026 Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
~~~

## anomalyco/models.dev

The model catalogue data in `next/internal/catalogue/snapshot.json`, generated by
`next/internal/catalogue/gen.go` from <https://models.dev/api.json>
(<https://github.com/anomalyco/models.dev>). `snapshot.json` keeps a filtered snapshot of the four
providers' model token limits and capabilities and refreshes it daily into `state/catalogue.json`.
Licence text, from `LICENSE` in `anomalyco/models.dev`:

~~~text
MIT License

Copyright (c) 2025 models.dev

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
~~~
