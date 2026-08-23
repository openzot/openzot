<p align="center">
  <img width="96" height="96" alt="zot" src="https://github.com/user-attachments/assets/44908dda-fa7c-4698-815a-82343cf54a44" />
</p>

<h1 align="center">zot</h1>

<p align="center">
  <strong>Stop promting. Start shipping.</strong>
</p>

<p align="center">
  <img width="920" alt="zot running a work order" src="https://github.com/user-attachments/assets/d12de01c-f13e-451c-93a3-d025b5b39dc6" />
</p>

zot is an automated software factory in a single ~11 MiB binary. It takes a
work order - an objective, the acceptance criteria that define done, the
constraints to hold - and plans, edits, builds, tests and fixes until the work
is done. No chat loop, no approving each edit, no hosted engine, no telemetry.
It talks straight to any OpenAI-compatible model provider with your own key.

## Install

```bash
curl -fsSL https://zot.im/install.sh | bash
```

That fetches the latest release for your platform (Linux or macOS, amd64 or
arm64), verifies its checksum, and puts `zot` in `~/.local/bin`. Pin a version
with `ZOT_VERSION=vX.Y.Z`, or change the directory with `ZOT_INSTALL_DIR`.

Prefer to do it by hand? Grab a tarball from the
[releases page](https://github.com/openzot/openzot/releases), pull the
[container image](docs/docker.md) (`ghcr.io/openzot/openzot`), or
[build from source](docs/development.md).

## Use

zot defaults to the `zai` provider running `glm-5.2`. Export a key, write an
order, hand it over:

```bash
export ZAI_API_KEY="…"
zot new "add input validation to the signup handler and a test"
zot
```

`zot new` writes a small YAML order under `.zot/orders/`; edit its acceptance
criteria, then a bare `zot` runs everything outstanding. `zot --watch` turns
the folder into a drop box. Any OpenAI-compatible provider works -
`--provider anthropic`, a local `--provider ollama`, a gateway, a custom
endpoint - see [providers](docs/providers.md).

## Why zot

- **Orders, not prompts.** One brief in, finished work out - and orders are
  files, so they compose, queue, and stream. [→ work orders](docs/orders.md)
- **Nothing in the way.** No hosted engine, no account, no telemetry. Your
  key, your provider, your machine. [→ philosophy](docs/philosophy.md)
- **Built to run unattended.** Context compaction, loop detection, settle mode,
  resumable session logs. [→ how it works](docs/how-it-works.md)
- **Orchestration is content.** No sub-agent framework; an agent can call
  `zot` again, and skills say when. [→ sub-agents](docs/how-it-works.md#sub-agents-and-coordination)

Watch it work: **[the arcade](https://openzot.github.io/arcade/)** ships a new
browser game every 30 minutes from one standing order, with no human in the
loop.

## ⚠️ Safety

zot has real file-write and shell access from `--dir`, and `--dir` is not a
sandbox. Point it at a disposable checkout, or run the
[container image](docs/docker.md). Read [safety](docs/safety.md) first.

## Documentation

- [docs/orders.md](docs/orders.md) - work orders, `zot new`, the book, watch mode
- [docs/providers.md](docs/providers.md) - providers, credentials, gateways, custom endpoints
- [docs/configuration.md](docs/configuration.md) - config file, flags, controls, sessions, `AGENTS.md` & skills
- [docs/how-it-works.md](docs/how-it-works.md) - the harness, what sits under it, sub-agents
- [docs/safety.md](docs/safety.md) - what zot can touch and how to bound it
- [docs/docker.md](docs/docker.md) - running the container image
- [docs/development.md](docs/development.md) - building from source, the codebase map
- [docs/portable-config.md](docs/portable-config.md) - baking the configuration into the binary
- [docs/philosophy.md](docs/philosophy.md) - why zot exists, the arcade, status
- [CHANGELOG.md](CHANGELOG.md) · [RELEASES.md](RELEASES.md)

## Ecosystem

| Project                                       | Role                                                                      |
| --------------------------------------------- | ------------------------------------------------------------------------- |
| [Arcade](https://openzot.github.io/arcade/)   | A live zot factory shipping one browser game every 30 minutes, unattended |
| [Rook](https://github.com/pdparchitect/rook)  | A fully automated offensive security harness                              |
| [Pion](https://github.com/pdparchitect/pion)  | A defensive AI security harness for automatic monitoring and incident prevention |
| [Pantalk](https://github.com/pantalk/pantalk) | Connect coding agents to the chat platforms people already use            |
| [MCPShim](https://github.com/mcpshim/mcpshim) | Turn MCP servers and HTTP APIs into standard CLI commands                 |
| [crmkit](https://github.com/crmkit/crmkit)    | Give agents a shared CRM and system of record over HTTP or MCP            |

## Status

zot is **0.x** and in active use. Flags, config and behavior may change before
1.0 - pin a version and skim the [changelog](CHANGELOG.md) before upgrading.
Small, focused pull requests are welcome; anything large is worth an issue first.
