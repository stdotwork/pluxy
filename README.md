# pluxy

pluxy (plug + proxy) is a proxy that serves OpenAI-, Gemini-, Claude- and Codex-compatible APIs
from your API keys and subscription sign-ins. It has three parts, all in `next/`:

- the core, a Go server that hands each request to a provider extension: a separate program for
  one kind of upstream, such as Codex, Claude or Gemini;
- a web panel, which the core serves at `/panel/` while its management API is on;
- a desktop app that runs or connects to a core, on Linux for now.

| Folder | Component |
| --- | --- |
| `next/` | The core, its extension protocol and the provider extensions ([README](next/README.md)) |
| `next/ui/` | The UI kit: the web panel, and the pages the desktop app shares with it ([README](next/ui/README.md)) |
| `next/app/` | The desktop app ([README](next/app/README.md)) |
| `extensions/app/` | The old desktop app. It stays until the new app runs on Windows and macOS. |

See [AGENTS.md](AGENTS.md) for how to build and test each folder and how changes are made, and
[docs/design.md](docs/design.md) for why the new core is built the way it is.

## Credits and license

pluxy started as Soul4723's forks of router-for-me's
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) core,
[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) desktop app and
[Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)
web panel. The new core and panel in `next/` replaced the core and panel forks, which have been
removed. The MIT notices of all three, and of the Pi adapter in `next/app-tools/pi-setup/adapter/`,
with the files `next/` keeps or adapts from each, are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The old desktop app also keeps its MIT `LICENSE`
in `extensions/app/`. The root license is not decided yet.
