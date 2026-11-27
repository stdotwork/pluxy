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
  `next/extensions/xai/patch.go` and `next/format/internal/clienttools/clienttools.go` are the
  constants of `internal/client/codex/apply-patch/tool.go` byte for byte, and the description each
  builds follows that file's `Description`;
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
- adapted: the audio format and MIME type table of `next/format/internal/audioinput/audioinput.go`,
  from `openAIInputAudioMimeType` in
  `internal/translator/gemini/openai/chat-completions/gemini_openai_request.go`, and its inline
  audio data URL, which that translator builds in `geminiInlineDataPart`;
- follows: the reading of a Responses `input_audio` part in either spelling (flat `data` and
  `format`, or inside `input_audio`), from `responsesInputAudioToChatPart` in
  `internal/translator/openai/openai/responses/openai_openai-responses_request.go`;
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

The desktop app formerly in this repository, from <https://github.com/router-for-me/EasyCLIProxyAPI>.
It was removed when the desktop app in `next/app` replaced it. `next/` keeps from it, all in Rust or
TypeScript as the original:

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

Other app tools in `next/app-tools/` follow the old app's rules for the files of the agents they set up
or read (for example `usage-claude`, `lib/pi-sessions` and `claude-desktop`): the rules are the
original's, the code is written afresh.

Licence text, as it was in the app's `LICENSE`:

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

## pingdotgg/t3code

The AI coding workspace app T3 Code, from <https://github.com/pingdotgg/t3code>, at commit
`2a411317d9b7341547e5acb3e9bceb7e6195c5a2`. `next/ui` loads T3 Code theme files unchanged using
constants and palette logic from it:

- copied: the 61 color role names (`T3_COLOR_ROLES`) and the 61 CSS custom property names
  (`APP_THEME_VARIABLES`) in `next/ui/kit/theme/t3.ts`, from `packages/shared/src/themePalettes.ts`
  and `apps/web/src/themePalette.ts`; and the example theme in
  `next/ui/tests/fixtures/t3-docs-nightfall.json`, from `docs/user/appearance.md`;
- adapted: `themeIdFromName` and `T3_ID_PATTERN` in `next/ui/kit/theme/t3.ts`, and
  `createVividThemeColors` with its color and contrast helpers in `next/ui/kit/theme/t3palette.ts`,
  from `apps/web/src/themePalette.ts` (replacing the `culori` dependency with a local parser for
  hex, `rgb()`/`rgba()` and `oklch()`, and omitting `getDefaultThemeColors`); and
  `next/ui/tests/fixtures/t3-grove-exported.json` from `GROVE_THEME` in
  `packages/shared/src/themePalettes.ts`;
- follows: the T3 Code theme file dialect and seed/variant rules in
  `next/ui/kit/theme/validate.ts`, following `parseThemeFile` in `apps/web/src/themePalette.ts`
  under pluxy's refusal and warning contract.

Licence text, as it is in `LICENSE`:

~~~text
MIT License

Copyright (c) 2026 T3 Tools Inc.

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

## openai/codex

The Codex CLI, from <https://github.com/openai/codex> (commit
`39c29c3bb620f91e84a3bb266bb496dfb697c101`). `next/` keeps from it:

- copied: `codexSummarizationPrompt` and `codexSummaryPrefix` in
  `next/extensions/gemini/compact.go` and `next/extensions/openai-compatible/compact.go`, from
  `codex-rs/prompts/templates/compact/prompt.md` and
  `codex-rs/prompts/templates/compact/summary_prefix.md`.

Attribution from `NOTICE`:

~~~text
OpenAI Codex
Copyright 2025 OpenAI
~~~

Licence text, as it is in `LICENSE`:

~~~text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

1.  Definitions.

    "License" shall mean the terms and conditions for use, reproduction,
    and distribution as defined by Sections 1 through 9 of this document.

    "Licensor" shall mean the copyright owner or entity authorized by
    the copyright owner that is granting the License.

    "Legal Entity" shall mean the union of the acting entity and all
    other entities that control, are controlled by, or are under common
    control with that entity. For the purposes of this definition,
    "control" means (i) the power, direct or indirect, to cause the
    direction or management of such entity, whether by contract or
    otherwise, or (ii) ownership of fifty percent (50%) or more of the
    outstanding shares, or (iii) beneficial ownership of such entity.

    "You" (or "Your") shall mean an individual or Legal Entity
    exercising permissions granted by this License.

    "Source" form shall mean the preferred form for making modifications,
    including but not limited to software source code, documentation
    source, and configuration files.

    "Object" form shall mean any form resulting from mechanical
    transformation or translation of a Source form, including but
    not limited to compiled object code, generated documentation,
    and conversions to other media types.

    "Work" shall mean the work of authorship, whether in Source or
    Object form, made available under the License, as indicated by a
    copyright notice that is included in or attached to the work
    (an example is provided in the Appendix below).

    "Derivative Works" shall mean any work, whether in Source or Object
    form, that is based on (or derived from) the Work and for which the
    editorial revisions, annotations, elaborations, or other modifications
    represent, as a whole, an original work of authorship. For the purposes
    of this License, Derivative Works shall not include works that remain
    separable from, or merely link (or bind by name) to the interfaces of,
    the Work and Derivative Works thereof.

    "Contribution" shall mean any work of authorship, including
    the original version of the Work and any modifications or additions
    to that Work or Derivative Works thereof, that is intentionally
    submitted to Licensor for inclusion in the Work by the copyright owner
    or by an individual or Legal Entity authorized to submit on behalf of
    the copyright owner. For the purposes of this definition, "submitted"
    means any form of electronic, verbal, or written communication sent
    to the Licensor or its representatives, including but not limited to
    communication on electronic mailing lists, source code control systems,
    and issue tracking systems that are managed by, or on behalf of, the
    Licensor for the purpose of discussing and improving the Work, but
    excluding communication that is conspicuously marked or otherwise
    designated in writing by the copyright owner as "Not a Contribution."

    "Contributor" shall mean Licensor and any individual or Legal Entity
    on behalf of whom a Contribution has been received by Licensor and
    subsequently incorporated within the Work.

2.  Grant of Copyright License. Subject to the terms and conditions of
    this License, each Contributor hereby grants to You a perpetual,
    worldwide, non-exclusive, no-charge, royalty-free, irrevocable
    copyright license to reproduce, prepare Derivative Works of,
    publicly display, publicly perform, sublicense, and distribute the
    Work and such Derivative Works in Source or Object form.

3.  Grant of Patent License. Subject to the terms and conditions of
    this License, each Contributor hereby grants to You a perpetual,
    worldwide, non-exclusive, no-charge, royalty-free, irrevocable
    (except as stated in this section) patent license to make, have made,
    use, offer to sell, sell, import, and otherwise transfer the Work,
    where such license applies only to those patent claims licensable
    by such Contributor that are necessarily infringed by their
    Contribution(s) alone or by combination of their Contribution(s)
    with the Work to which such Contribution(s) was submitted. If You
    institute patent litigation against any entity (including a
    cross-claim or counterclaim in a lawsuit) alleging that the Work
    or a Contribution incorporated within the Work constitutes direct
    or contributory patent infringement, then any patent licenses
    granted to You under this License for that Work shall terminate
    as of the date such litigation is filed.

4.  Redistribution. You may reproduce and distribute copies of the
    Work or Derivative Works thereof in any medium, with or without
    modifications, and in Source or Object form, provided that You
    meet the following conditions:

    (a) You must give any other recipients of the Work or
    Derivative Works a copy of this License; and

    (b) You must cause any modified files to carry prominent notices
    stating that You changed the files; and

    (c) You must retain, in the Source form of any Derivative Works
    that You distribute, all copyright, patent, trademark, and
    attribution notices from the Source form of the Work,
    excluding those notices that do not pertain to any part of
    the Derivative Works; and

    (d) If the Work includes a "NOTICE" text file as part of its
    distribution, then any Derivative Works that You distribute must
    include a readable copy of the attribution notices contained
    within such NOTICE file, excluding those notices that do not
    pertain to any part of the Derivative Works, in at least one
    of the following places: within a NOTICE text file distributed
    as part of the Derivative Works; within the Source form or
    documentation, if provided along with the Derivative Works; or,
    within a display generated by the Derivative Works, if and
    wherever such third-party notices normally appear. The contents
    of the NOTICE file are for informational purposes only and
    do not modify the License. You may add Your own attribution
    notices within Derivative Works that You distribute, alongside
    or as an addendum to the NOTICE text from the Work, provided
    that such additional attribution notices cannot be construed
    as modifying the License.

    You may add Your own copyright statement to Your modifications and
    may provide additional or different license terms and conditions
    for use, reproduction, or distribution of Your modifications, or
    for any such Derivative Works as a whole, provided Your use,
    reproduction, and distribution of the Work otherwise complies with
    the conditions stated in this License.

5.  Submission of Contributions. Unless You explicitly state otherwise,
    any Contribution intentionally submitted for inclusion in the Work
    by You to the Licensor shall be under the terms and conditions of
    this License, without any additional terms or conditions.
    Notwithstanding the above, nothing herein shall supersede or modify
    the terms of any separate license agreement you may have executed
    with Licensor regarding such Contributions.

6.  Trademarks. This License does not grant permission to use the trade
    names, trademarks, service marks, or product names of the Licensor,
    except as required for reasonable and customary use in describing the
    origin of the Work and reproducing the content of the NOTICE file.

7.  Disclaimer of Warranty. Unless required by applicable law or
    agreed to in writing, Licensor provides the Work (and each
    Contributor provides its Contributions) on an "AS IS" BASIS,
    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
    implied, including, without limitation, any warranties or conditions
    of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
    PARTICULAR PURPOSE. You are solely responsible for determining the
    appropriateness of using or redistributing the Work and assume any
    risks associated with Your exercise of permissions under this License.

8.  Limitation of Liability. In no event and under no legal theory,
    whether in tort (including negligence), contract, or otherwise,
    unless required by applicable law (such as deliberate and grossly
    negligent acts) or agreed to in writing, shall any Contributor be
    liable to You for damages, including any direct, indirect, special,
    incidental, or consequential damages of any character arising as a
    result of this License or out of the use or inability to use the
    Work (including but not limited to damages for loss of goodwill,
    work stoppage, computer failure or malfunction, or any and all
    other commercial damages or losses), even if such Contributor
    has been advised of the possibility of such damages.

9.  Accepting Warranty or Additional Liability. While redistributing
    the Work or Derivative Works thereof, You may choose to offer,
    and charge a fee for, acceptance of support, warranty, indemnity,
    or other liability obligations and/or rights consistent with this
    License. However, in accepting such obligations, You may act only
    on Your own behalf and on Your sole responsibility, not on behalf
    of any other Contributor, and only if You agree to indemnify,
    defend, and hold each Contributor harmless for any liability
    incurred by, or claims asserted against, such Contributor by reason
    of your accepting any such warranty or additional liability.

END OF TERMS AND CONDITIONS

APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

Copyright 2025 OpenAI

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
~~~

## BerriAI/litellm

LiteLLM, from <https://github.com/BerriAI/litellm> (commit
`6245f94734938b0cfc25d208064a01806518040d`). `next/` keeps from it:

- adapted: in `next/format/internal/clienttools/clienttools.go`, the handling of a tool_search the
  client runs follows `litellm/responses/litellm_completion_transformation/client_tool_search.py`:
  deferred tools stay off the request until a search output loads them, and the two sentences of
  `searchOutput` are that file's `_search_output_text`.
- adapted: in `next/llm/overflow.go`, the list of upstream messages that say a prompt does not fit
  the model's context window, and the checks beside it, follow
  `is_error_str_context_window_exceeded` in `litellm/litellm_core_utils/exception_mapping_utils.py`,
  except the phrase `input context is too long`, which comes from Google's Gemini API error table.

Licence text, for content outside `litellm/enterprise/`, as it is in `LICENSE`:

~~~text
MIT License

Copyright (c) 2023 Berri AI

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

## cheaper-inference/OmniRoute

The AI gateway at <https://github.com/cheaper-inference/OmniRoute> (formerly
`diegosouzapw/OmniRoute`). `next/internal/server/keepalive.go`
*follows* the rule of its `open-sse/utils/sseHeartbeat.ts` for where a keep-alive may go in a
stream (the framing tracker: only between events, judged on line ends alone). Licence text, as it
is in the project's `LICENSE`:

~~~text
MIT License

Copyright (c) 2026 diegosouzapw

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

## IBM/plex

The typefaces IBM Plex Sans and IBM Plex Mono, from <https://github.com/IBM/plex>, which the UI's
default theme uses. `next/ui/kit/fonts/` holds five of their font files, copied byte for byte from
the Fontsource packages `@fontsource/ibm-plex-sans` 5.3.0 and `@fontsource/ibm-plex-mono` 5.3.0
(<https://fontsource.org>), which publish Latin subsets in WOFF2: Plex Sans 400, 500 and 600
(`plex-sans-400.woff2`, `plex-sans-500.woff2`, `plex-sans-600.woff2`; version 3.201 in their
own metadata) and Plex Mono 400 and 500 (`plex-mono-400.woff2`, `plex-mono-500.woff2`; version
2.3). The panel and the desktop app ship them as files. They are under the SIL Open Font License
1.1, not MIT. Licence text, from each package's `LICENSE`; the copyright lines differ and the
licence is the same:

~~~text
IBM Plex Sans: Copyright 2019 IBM Corp. All rights reserved.
IBM Plex Mono: Copyright 2017 IBM Corp. All rights reserved.

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
~~~

## rsms/inter

Inter, from <https://github.com/rsms/inter>, the typeface Inter, which the UI's default theme uses for
its body text (`font.body`). `next/ui/kit/fonts/` holds one of its font files,
`inter-latin-wght-normal.woff2`: the Latin subset of Inter's variable font (weights 100 to 900, normal
style), copied byte for byte from the Fontsource package `@fontsource-variable/inter` 5.3.0
(<https://fontsource.org>), its `files/inter-latin-wght-normal.woff2`. The font's own name table gives
its version as 4.001 (`Version 4.001;git-66647c0bb`); the package's metadata names Google Fonts' build
v20. The kit's build embeds it in the built stylesheet of the panel and the desktop app. It is under the
SIL Open Font License 1.1, not MIT. Licence text, from the package's `LICENSE`:

~~~text
Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter) Inter-Italic[opsz,wght].ttf: Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter)

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
~~~
