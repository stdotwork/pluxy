# Claude Desktop gateway configuration

Verified on 2026-10-22 against Claude Desktop for Windows 3.1623.1.0 and the official references:

- https://claude.com/docs/third-party/claude-desktop/configuration
- https://claude.com/docs/third-party/claude-desktop/gateway

## Local configuration contract

Windows stores local third-party configurations in `%LOCALAPPDATA%/Claude-3p/configLibrary/`.
macOS uses `~/Library/Application Support/Claude-3p/configLibrary/`; Linux uses its Claude-3p configuration directory.
`_meta.json` selects a saved `<id>.json` profile through `appliedId`.
These are JSON files with native booleans and arrays, not the string-encoded values required by Windows registry policy.

CPA owns a dedicated profile and writes the following connection settings:

```json
{
  "disableDeploymentModeChooser": true,
  "inferenceProvider": "gateway",
  "inferenceCredentialKind": "static",
  "inferenceGatewayAuthScheme": "bearer",
  "inferenceGatewayBaseUrl": "http://127.0.0.1:<configured-port>",
  "inferenceGatewayApiKey": "<configured-client-key>",
  "modelDiscoveryEnabled": false,
  "bootstrapEnabled": false,
  "inferenceModels": [
    {
      "name": "<exact-model-route>",
      "labelOverride": "<source-model>"
    }
  ]
}
```

The base URL is the proxy origin; Desktop appends `/v1/messages`.
Model IDs must match routes exposed by the proxy, not display labels. CPA's explicit list disables discovery, including resolution of bare tier aliases.
Model entries use `anthropicFamilyTier` and `isFamilyDefault` for recognized opus, sonnet, haiku, fable and mythos routes.
`supports1m` and `prefer1m` are written only for an explicitly selected 1M option.
User model extensions, including `maxEffort` and `minDesktopVersion`, remain preserved on matching entries.

An explicit static credential kind prevents old helper or interactive sign-in settings from replacing the proxy key.
CPA disables a bootstrap overlay in its profile and removes a competing `selfHostedUrl` while managing that profile.
Other user preferences and inactive credential settings remain intact.

In the installed 3.1623.1.0 implementation, `_meta.json.hybridPointer` takes precedence over `appliedId`.
This is an implementation detail, not a documented stable API. Applying the CPA profile clears the pointer, matching Desktop's local-apply behavior.
Integration history retains the original configuration for restoration; removing a dormant CPA integration must not disable an active hybrid deployment.

## Atomic updates and validation

Normal updates, template application and model-mapped backup restoration update proxy routes first.
Configuration writes retain the existing validation and rollback behavior: if writing client files fails, the proxy changes roll back.
The public model catalog is not a complete routing contract. Missing or delayed discovery entries must not block saving a valid model mapping or cause a rollback.
Saving does not poll model discovery or send an inference request. It does not validate upstream availability or model behavior.
Claude Desktop's own connection test may subsequently issue a Messages API request.

## Operational limits

- Fully quit and reopen Claude Desktop after changing configuration; editing a file does not establish that the running application has reloaded it.
- Managed OS policy can override the entire local configuration (with the app-behavior-only exception described by Anthropic). CPA does not rewrite enterprise policy. Use Desktop's Managed Configuration Report to diagnose policy precedence.
- A missing listener, rejected credentials, an absent model route and an upstream error are different failures. Do not infer a configuration-format change from a connection banner alone.

## Regression coverage

Tests cover explicit static credentials, conflicting bootstrap/session-host settings, hybrid switching and restoration, preserving user preferences, current model tiers, successful alias saves with an empty public model catalog, and rollback when configuration writes fail.
The isolated `v8_claude_desktop_and_code_share_persisted_model_routes` test exercises a real proxy executable through `CPA_V8_TEST_CORE` without changing the user's running proxy.

## API-key routing repair and live verification (2026-10-22)

Desktop source resolution, identity lookup and managed-route cleanup enumerate all eight
`V8_PROVIDER_FAMILIES`. The older generic reasoning-alias section list does not include
xAI, Meta, Vertex or Interactions API-key sources and must not drive Desktop routing.
A managed alias with matching model names is not a no-op when the selected source
resolves to a different API-key provider. Repair it on the selected provider before
writing the Desktop profile. Alias transactions still preserve API credentials and
roll back route changes if the client write fails.

Live verification used Claude Desktop 3.1623.1.0 and CLIProxyAPI 8.0.36 at
`127.0.0.1:18317`, with a separate configuration and OAuth directory. Production
port 8317 was not changed. The application transaction generated the test profile
and persisted `grok-4.7-build-fast -> claude-sonnet-5-cc` on the xAI API-key source.
After restarting Desktop, the unavailable-model banner disappeared. A real message
asking `What is 2 + 3?` received `5.` in the Desktop conversation. The isolated
request log recorded HTTP 200 for `/v1/messages?beta=true`, a text response and
`message_stop`. A follow-up asking to add 7 received `12`, also with HTTP 200 and
a completed stream. The final suite passed 973 tests (12 opt-in tests ignored);
the isolated profile helper also passed explicitly. The Windows release build succeeded.
This verifies a short streamed conversation, not 1M-token capacity,
Cowork or arbitrary tool use. The old selected `grok-4.6` was unavailable in the
active provider; this test used the available `grok-4.7-build-fast` model.

The opt-in `desktop_e2e_prepare_isolated_gateway_profile` helper requires an explicit
`CPA_DESKTOP_E2E_CONFIG` and rejects port 8317. It prepares configuration only;
Desktop UI verification is a separate manual/computer-use step. Runtime test files
contain credentials and stay in ignored `bin-work`, never in this document.

## Review follow-up: stable routing and excluded sources

An unchanged Desktop save retains its selected managed alias during cleanup and
keeps a valid existing API-key provider, even if another provider publishes the
same model ID. Disabled providers and exclusions are checked for both the upstream
model and client alias. A stale managed OAuth route for a configured API-key model
cannot substitute for an excluded API source without a matching OAuth channel/model
definition. Explicit legacy OAuth routes remain supported.

Regression coverage includes the two review reproductions, wildcard exclusions,
disabled API providers, confirmed OAuth alternatives, and an excluded upstream
with an existing API-key alias. The final suite passed 977 tests with 12 opt-in
tests ignored. The isolated profile application test passed separately. After
reapplying with the updated code on port 18317, a real Desktop message asking
`8 + 9` received `17`.
