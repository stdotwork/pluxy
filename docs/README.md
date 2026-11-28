# pluxy docs

pluxy is a proxy that runs on your machine and serves the OpenAI, Anthropic, Gemini and Codex APIs
from the accounts you already have. These pages say how to set it up, use it and extend it.

## Start here

- [Quick start](../README.md#quick-start): build pluxy, add an account and send a first request.
- [Setup](setup.md): build from source, start core, where its files are, run it in a container.
- [Clients](clients.md): point Claude Code, Codex, OpenCode, Pi or another client at pluxy.
- [Troubleshooting](troubleshooting.md): what an error means and what to do about it.

## Guide

- [Providers](providers.md): the providers pluxy serves, how to sign in to each, and their options.
- [Accounts and routing](routing.md): how a request picks an account, blocks, fallback chains,
  window ignition, proxies, headers, payload rules and model names.
- [Extensions](extensions.md): install, update, turn off and remove provider extensions and packages.
- [Usage and logs](usage-and-logs.md): the usage record, log files, request logs and profiling pages.
- [Web panel](panel.md): the panel core serves at `/panel/`.
- `app.md`: the desktop app.
- [Moving from CLIProxyAPI](moving-from-cpa.md): bring a CLIProxyAPI setup into pluxy.

## Reference

- [Configuration](config.md): where the config is, every key, every `PLUXY_` environment variable,
  and what reloads without a restart.
- [Command line](cli.md): every `pluxy` command and flag.
- [Management API](management-api.md): the HTTP API the panel, the app and the `pluxy` command use.
- [Translation](translation.md): how requests and replies are translated between the client APIs and upstreams.
- [UI terms](terms.md): the words the panel and the app use for quota, limits, accounts and usage.

## Extend pluxy

- [Extension tutorial](../next/protocol/TUTORIAL.md): write a provider extension, from an empty
  folder to one that passes the conformance kit.
- [Extension protocol](../next/protocol/README.md): the protocol between core and extensions, the
  package manifest and release archives.
- [Theme contract](../next/ui/themes/README.md): the theme files the panel and the app read.
- [Extension pages](../next/ui/extension-pages/README.md): pages and settings forms an extension
  package brings to the panel and the app.
- [App package contract](../next/app/app-host/README.md): what a package adds to the desktop app,
  and the contract for its native tools.

## Design notes

- [Design](design/design.md): why pluxy is built this way, and what core keeps apart from extensions.
- [Plans](design/plans.md): confirmed plans that are not built yet.
- `app-design.md`: how the desktop app works inside.
- [Corpus](design/corpus.md): how the differential test corpus holds core to recorded replies.
- [Performance](design/perf.md): the time and memory pluxy adds, and the budgets CI holds it to.

## Contributing

- [AGENTS.md](../AGENTS.md): the repository's layout, build and test commands, and how changes flow.
- [Code map](map.md): where to start in the code for a kind of change, and the test fakes.
- [CI](ci.md): the GitHub Actions workflows and the runners they use.
