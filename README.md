<p align="center">
  <img width="96" height="96" alt="zot-icon-dark" src="https://github.com/user-attachments/assets/44908dda-fa7c-4698-815a-82343cf54a44" />
</p>

<h1 align="center">zot</h1>

**Hand it a software task and get back finished, tested code.** zot is an
automated software factory in a single binary - an autonomous coding harness that
plans, edits, runs, and verifies until the work is done.

<p align="center">
  <img width="1504" height="1080" alt="zot demo" src="https://github.com/user-attachments/assets/d12de01c-f13e-451c-93a3-d025b5b39dc6" />
</p>

## The point

zot is not another coding agent you sit with and steer prompt by prompt. Its
reason to exist is **fully autonomous software work**: give it a goal, walk away,
and come back to a finished result. It will sometimes fall short of that ideal,
especially while it is 0.x, but the ideal is the design constraint. We are not
building workflows or features that contradict it.

If what you want is an interactive coding agent, use
[Codex](https://openai.com/codex/),
[Claude Code](https://www.anthropic.com/claude-code),
[Cursor](https://www.cursor.com/), or one of the other excellent tools built for
that way of working.

## What you get

Give it a brief and walk away:

```bash
zot "add rate limiting to the API, with tests"
```

zot reads the repo, writes and edits the code, runs the build and the tests, and
fixes what it broke - on its own, from that one brief to a recorded outcome. When
it stops you have a changed working tree and a full session log of every step it
took. No chat loop, no approving each edit: one brief in, finished work out.

## zotui

The release also includes **zotui**, a browser command center for operating zot
as reusable workers. Choose a repository, environment, provider and model, give
the worker its mission, then launch runs on demand or on a schedule and follow
their output and history from the browser. Workers can run in disposable local
Docker containers or on configured remote compute.

https://github.com/user-attachments/assets/304b4f32-c634-4936-a1ec-66bdd792bd66

zotui keeps the same autonomy boundary: it dispatches and observes complete
runs; it does not turn zot into a chat-driven coding agent. See
[Configuring zotui](docs/zotui-configuration.md) for setup and deployment.

## Why zot exists

Most coding tools optimize the conversation between a developer and an agent.
zot optimizes the production run: it drives the whole job from one brief, without
a follow-up prompt at every step and without a hosted engine.

It talks straight to a model provider over the OpenAI-compatible API - OpenAI,
Anthropic, Groq, Mistral, DeepSeek, OpenRouter, a local Ollama, or anything else
that speaks the same API - so all you need is a provider key. No account beyond
the provider's own, and nothing is sent anywhere except the provider you
configure.

## Quickstart

Grab a prebuilt binary from the
[releases page](https://github.com/openzot/openzot/releases) - no toolchain
required:

```bash
VERSION=vX.Y.Z; OS=linux ARCH=amd64      # e.g. darwin/arm64 on Apple Silicon
curl -L "https://github.com/openzot/openzot/releases/download/${VERSION}/zot-${VERSION}-${OS}-${ARCH}.tar.gz" | tar xz
mv "zot-${VERSION}-${OS}-${ARCH}"/{zot,zotui} ~/.local/bin/   # or any directory on your PATH
```

zot defaults to the **`zai`** provider running **`glm-5.2`**. Export a key and
give it a job:

```bash
export ZAI_API_KEY="…"
zot "add input validation to the signup handler and a test"
```

Any OpenAI-compatible provider works - `--provider anthropic --model
claude-5-sonnet`, a local `--provider ollama`, gateways, or a custom endpoint; see
[docs/providers.md](docs/providers.md). To build from source or run the container
image, see [docs/development.md](docs/development.md) and
[docs/docker.md](docs/docker.md).

## How it works

The harness is zot's own, in the [`agent`](agent) package:

- `agent.ExecuteWithTools` runs the model in a loop - it calls tools, sees the
  results, and goes again - until it records an outcome (`success` / `failure`)
  or a budget or guard stops it.
- `agent.DefaultTools()` gives it the toolbox: `read`, `write`, `list` and
  `shell` for the work, plus `plan` and `progress` to structure and narrate it.

That package is importable, so the same engine drives more than coding:
[Rook](https://github.com/pdparchitect/rook), an AI bug-hunting and security-audit
agent, is built on it with its own toolset and skills.

What sits under that is the part worth knowing about:

- **Thread assembly** fits the conversation to the model's context window,
  newest-first, keeping tool calls paired with their results.
- **Context strategy** decides what happens as that window fills. `compact` (the
  default) summarises the older history into a checkpoint with a model call, so a
  long run keeps a condensed memory of its early turns instead of losing them;
  `truncate` simply drops the oldest messages to fit. A checkpoint is preserved
  verbatim and never re-summarised, and an outright provider rejection falls back
  to a no-model summary and retries. Configurable - see
  [configs/zot.example.yaml](configs/zot.example.yaml).
- **Loop detection** notices when the agent has stopped making progress - four
  overlapping heuristics, because the obvious one (repeated messages) silently
  misses reasoning models, which interleave a thought between every tool call.
- **Settle mode** ends a run when the agent _records_ an outcome, not when its
  prose happens to sound final. An answer containing "task completed" is not an
  ending. Note the boundary, though: settle mode checks that the agent _declared_
  done, not that the work _is_ done. The agent verifies its own work by running
  the tests (its instructions tell it to); zot does not yet independently gate
  the outcome on a check of its own.
- **Session logs** record every run to disk as it happens, so a run nobody
  watched is still answerable afterwards - and so it can be picked up again with
  `--resume` (see [docs/configuration.md](docs/configuration.md#sessions)).

`zot` itself is a read-only [Bubble Tea](https://github.com/charmbracelet/bubbletea)
viewer over that run - it renders the event stream into a scrollable log and has
no text input, because the run is autonomous.

## Sub-agents and coordination

zot has **no native sub-agent primitive, by design.** There is no `spawn`, no
built-in supervisor, no fixed orchestration graph baked into the engine - and
that absence is deliberate, not a gap waiting to be filled. A fixed hierarchy
would decide, in advance, how work should be divided for every task; most tasks
do not divide the way the framework guessed.

What the engine gives instead is the raw capability, and it lets the agent decide
how to use it:

- **An agent can call into itself.** It has a `shell` tool, so it can invoke
  `zot` again - a fresh run with its own brief, its own working directory, its
  own budget - and read back the result. Delegating a self-contained piece of
  work to a clean context is a shell command, not a special API. Direct
  instructions in your `AGENT.md`, or a skill, are what tell it when that is
  worth doing.
- **Agent-to-agent communication is left open, too.** zot does not prescribe a
  message bus or a protocol. Two runs coordinate through whatever they already
  share - the filesystem (a scratch file, a work queue as a directory of tasks),
  a git branch, an HTTP endpoint, a channel daemon. The mechanism is a choice
  the task makes, not one the engine imposes.

The intent is that **the agents themselves figure out how to organise and
communicate**, from the task in front of them, the context in the repository,
and any guidance a relevant skill supplies. Encode the pattern you want - a
map/reduce fan-out, a reviewer that re-runs the worker, a pipeline of
single-purpose runs - as a **skill** (`<name>/SKILL.md`), and it becomes
available exactly when the model judges it relevant, without changing the
engine. Orchestration is content, not framework.

## ⚠️ Safety

`zot` is fully autonomous and has **real** file-write and shell-exec access
from `--dir`. The flag changes the process working directory; it is **not a
filesystem sandbox**. Absolute paths and shell commands retain all permissions
of the zot process. Point it at a scratch directory or a disposable git checkout
you are happy for it to change - not your home directory.

Configured provider credentials are resolved into zot's in-memory configuration
and then removed from the process environment before the agent starts, so its
shell commands do not inherit those API keys. Other secrets already present in
the environment or readable from disk remain accessible to those commands.

The published [container image](docs/docker.md) is the practical way to bound
this: the agent can only touch the volume you mounted, and `docker run` gives you
the rest of the levers (read-only root, dropped capabilities, resource limits) in
one place.

## Status

zot is **0.x**: functional and in active use, with improvements landing release
to release. Until 1.0 the CLI flags, config, and behavior may still change
between versions - pin a version and skim the [changelog](CHANGELOG.md) before
upgrading.

## Documentation

- [docs/providers.md](docs/providers.md) - providers, credentials, gateways, custom endpoints, the Responses API
- [docs/configuration.md](docs/configuration.md) - config file, flags, controls, sessions, `AGENT.md` & skills
- [docs/zotui-configuration.md](docs/zotui-configuration.md) - zotui repos, compute, models, environments, Vercel Sandbox, and local Docker setup
- [docs/development.md](docs/development.md) - building from source, release vs developer builds, the codebase map
- [docs/portable-config.md](docs/portable-config.md) - baking the configuration into the binary
- [docs/docker.md](docs/docker.md) - running the container image
- [CHANGELOG.md](CHANGELOG.md) · [RELEASES.md](RELEASES.md)

## Ecosystem

| Project                                       | Role                                                                               |
| --------------------------------------------- | ---------------------------------------------------------------------------------- |
| [Rook](https://github.com/pdparchitect/rook)  | A fully automated offensive security harness                                       |
| [Pion](https://github.com/pdparchitect/pion)  | A defensive AI security harness for automatic mornitoring, and incident prevention |
| [Pantalk](https://github.com/pantalk/pantalk) | Connect coding agents to the chat platforms people already use                     |
| [MCPShim](https://github.com/mcpshim/mcpshim) | Turn MCP servers and HTTP APIs into standard CLI commands                          |
| [crmkit](https://github.com/crmkit/crmkit)    | Give agents a shared CRM and system of record over HTTP or MCP                     |
