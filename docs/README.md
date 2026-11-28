# pluxy docs

pluxy is a proxy that runs on your machine and serves the OpenAI, Anthropic, Gemini and Codex APIs
from the accounts you already have. These pages say how to set it up, use it and extend it.

## Start here

- [Quick start](../README.md#quick-start): build pluxy, add an account and send a first request.
- [Setup](setup.md): build from source, start core, where its files are, run it in a container.
- [Providers](providers.md): the providers pluxy serves, how to sign in to each, and their options.
- [Clients](clients.md): point Claude Code, Codex, OpenCode, Pi or another client at pluxy.
- [Troubleshooting](troubleshooting.md): what an error means and what to do about it.

## Guide

- [Accounts and routing](routing.md): how a request picks an account, blocks, fallback chains,
  window ignition, proxies, headers, payload rules and model names.
- [Extensions](extensions.md): install, update, turn off and remove provider extensions and packages.
- [Usage and logs](usage-and-logs.md): the usage record, log files, request logs and profiling pages.
- [Web panel](panel.md): the panel core serves at `/panel/`.
- [Desktop app](app.md): the desktop app.
- [Moving from CLIProxyAPI](moving-from-cpa.md): bring a CLIProxyAPI setup into pluxy.

## Reference

- [Configuration](config.md): where the config is, every key, every `PLUXY_` environment variable,
  and what reloads without a restart.
- [config.yaml keys](config-yaml.md): CLIProxyAPI `config.yaml` keys and auth-file fields mapped to
  pluxy.
- [Command line](cli.md): every `pluxy` command and flag.
- [Management API](management-api.md): the HTTP API the panel, the app and the `pluxy` command use.
- [Translation](translation.md): how requests and replies are translated between the client APIs and upstreams.

## Extend pluxy

- [Extension tutorial](extension-tutorial.md): write a provider extension, from an empty
  folder to one that passes the conformance kit.
- [Extension protocol](protocol.md): the protocol between core and extensions, the
  package manifest and release archives.
- [Theme contract](themes.md): the theme files the panel and the app read.
- [Extension pages](extension-pages.md): pages and settings forms an extension
  package brings to the panel and the app.
- [App package contract](app-packages.md): what a package adds to the desktop app,
  and the contract for its native tools.
- [App package tutorial](app-package-tutorial.md): build a first web package or native tool
  for the desktop app.
- [App tools](app-tools.md): reference for the desktop app's native tools and shared libraries.

## Design notes

- [Design](design/design.md): why pluxy is built this way, and what core keeps apart from extensions.
- [Plans](design/plans.md): confirmed plans that are not built yet.
- [Desktop app internals](design/app-design.md): how the desktop app works inside.
- [Corpus](design/corpus.md): how the differential test corpus holds core to recorded replies.
- [Performance](design/perf.md): the time and memory pluxy adds, and the budgets CI holds it to.

## Contributing

- [AGENTS.md](../AGENTS.md): the repository's layout, build and test commands, and how changes flow.
- [Code map](map.md): where to start in the code for a kind of change, and the test fakes.
- [CI](ci.md): the GitHub Actions workflows and the runners they use.
- [UI terms](terms.md): the words the panel and the app use for quota, limits, accounts and usage.
