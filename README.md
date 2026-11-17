# pluxy

pluxy (plug + proxy) is a monorepo combining Soul4723's forks of router-for-me's
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) core,
[EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) desktop app, and
[Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) web panel.
The core fork has since been replaced by the new core in `next/`, and the panel fork by the web
panel in `next/ui`.

| Folder | Component |
| --- | --- |
| `extensions/app/` | Desktop app |
| `next/` | New core and extension protocol (in progress) |

See [AGENTS.md](AGENTS.md) for how to build and test each folder and how changes are made.

`extensions/app/` keeps its upstream MIT `LICENSE`. The notices of the removed old core and old web
panel are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The root license is not decided yet.
