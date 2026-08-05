<p align="center">
  <img src="https://zot.im/icon-dark.svg" alt="zot" width="96" height="96" />
</p>

<h1 align="center">zot</h1>

**An automated software factory in a single binary.** Give it a software job and
its autonomous coding harness plans, edits, runs, and verifies your code until
the work is complete.

<p align="center">
  <img width="1504" height="1080" alt="zot demo" src="https://github.com/user-attachments/assets/d12de01c-f13e-451c-93a3-d025b5b39dc6" />
</p>

## Status

zot is **0.x**: functional and in active use, with improvements landing release
to release. Until 1.0 the CLI flags, config, and behavior may still change
between versions - pin a version and skim the [changelog](CHANGELOG.md) before
upgrading.

## Why zot exists

Most coding tools optimize the conversation between a developer and an agent.
zot optimizes the production run. A software brief goes in, the repository moves
toward the requested outcome, and a verified working tree comes out.

The factory is powered by an autonomous coding harness that runs entirely in the
binary. It drives the whole loop (plan → act → observe → verify → exit) from one
brief, without waiting for follow-up prompts at every step - and without a hosted
engine. zot talks straight to a model provider over the OpenAI-compatible API, so
all you need is a provider key: OpenAI, Anthropic, Groq, Mistral, DeepSeek,
OpenRouter, a local Ollama, or anything else that speaks the same API. No
account is required beyond the provider's own, and nothing is sent anywhere
except the provider you configure.

## The factory model

| Stage  | What zot does                                                    |
| ------ | ---------------------------------------------------------------- |
| Brief  | Accepts one task plus the repository's `AGENT.md` and skills     |
| Plan   | Breaks the requested outcome into executable work                |
| Build  | Reads, writes, and edits files, then runs commands in the repo   |
| Verify | Inspects results and iterates when the job is not complete       |
| Output | Leaves the completed work in the working tree with a visible log |

## How it works

The harness is zot's own, in the [`agent`](agent) package:

- `agent.ExecuteWithTools` runs the model in a loop - _plan → act → observe →
  progress → exit_ - until it records an outcome or hits a budget.
- `agent.DefaultTools()` gives it the coding toolbox: `read`, `write`, `list`
  and `shell`.

What sits under that is the part worth knowing about:

- **Thread assembly** fits the conversation to the model's context window,
  newest-first, keeping tool calls paired with their results.
- **Compaction** condenses older history into a summary when the window fills,
  rather than dropping it.
- **Loop detection** notices when the agent has stopped making progress - four
  overlapping heuristics, because the obvious one (repeated messages) silently
  misses reasoning models, which interleave a thought between every tool call.
- **Settle mode** ends a run when the agent *records* an outcome, not when its
  prose happens to sound final. An answer containing "task completed" is not an
  ending.
- **Session logs** record every run to disk as it happens, so a run nobody
  watched is still answerable afterwards - and so it can be picked up again.

`zot` itself is a [Bubble Tea](https://github.com/charmbracelet/bubbletea)
front-end for that production run. It launches the harness in a goroutine and
renders the event stream (`ToolCallStart`, `ToolCallEnd`, `Iteration`, token
narration, `AgentExit`, …) into a scrollable, read-only viewport. The UI
deliberately has no text input.

## Prerequisites

- A key for whichever model provider you want to use
- Go 1.26+ - only if you build from source

## Provider credentials

zot defaults to the **`openai`** backend and reads `OPENAI_API_KEY`:

```bash
export OPENAI_API_KEY="sk-…"
zot "add input validation to the signup handler and a test"
```

Every built-in backend reads its own conventional variable - `ANTHROPIC_API_KEY`,
`GROQ_API_KEY`, `MISTRAL_API_KEY`, `DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`,
`TOGETHER_API_KEY`, `CEREBRAS_API_KEY`, `XAI_API_KEY`, `MOONSHOT_API_KEY`,
`ZAI_API_KEY`, `DASHSCOPE_API_KEY` - so switching provider is a flag:

```bash
export ANTHROPIC_API_KEY="sk-ant-…"
zot --backend anthropic "…"
```

A local model needs no key at all:

```bash
zot --backend ollama --model llama-4 "…"
```

Anything else that speaks the OpenAI chat-completions API works too - see
[Any other provider](#any-other-provider).

Any key can equally live in the config file
(`~/.config/zot/config.yaml`, or the path given to `--config`), including as a
`$ENV_VAR` reference so no secret is written to disk:

```yaml
# ~/.config/zot/config.yaml
backends:
  openai:
    api_key: '$OPENAI_API_KEY'
```

## Developer builds

`make build` produces a release binary. `make dev` produces a developer one, and
the difference is a security boundary rather than a convenience:

| | release (`make build`) | developer (`make dev`) |
| --- | --- | --- |
| reads `.env` from `--dir` | no | yes |

zot runs unattended with a provider key and a shell tool, so a released binary
must not take credentials from whatever directory it was pointed at - running it
against a repository you cloned to review would otherwise be enough to load a
stray committed `.env` into the process that is about to run commands. Released
builds read credentials from the config file and the real environment, both of
which you chose deliberately.

The switch is a build tag (`-tags dev`) and defaults to off, so a build that
forgets it loses a convenience rather than a boundary. `zot --version` prints
which kind you have.

## Install

### Download a release (recommended)

Grab a prebuilt binary from the
[releases page](https://github.com/openzot/openzot/releases) - no toolchain
required. Pick the archive for your platform (`linux-amd64`, `linux-arm64`,
`darwin-amd64`, `darwin-arm64`, `windows-amd64`):

```bash
VERSION=vX.Y.Z           # replace with the latest release tag
OS=linux ARCH=amd64      # e.g. darwin/arm64 on Apple Silicon
curl -L "https://github.com/openzot/openzot/releases/download/${VERSION}/zot-${VERSION}-${OS}-${ARCH}.tar.gz" | tar xz
mv "zot-${VERSION}-${OS}-${ARCH}/zot" ~/.local/bin/ # or any directory on your PATH
zot --version
```

### Build from source

Requires Go 1.26+.

```bash
git clone https://github.com/openzot/openzot
cd openzot
make                     # lists the targets
make build               # or: go build -o zot ./cmd/zot
./zot --version
```

`make` on its own prints the targets rather than assuming one, because `build`
and `dev` produce binaries that differ in what they will read from disk. `make
build` stamps the version in; `make test`, `make race`, `make cover`, `make vet`
and `make cross GOOS=… GOARCH=…` are also available.

### Docker

Official Linux amd64 and arm64 images are published to GitHub Container
Registry from the same version tag as the binary release. The agent works in
`/workspace` - mount the checkout you are happy for it to change there:

```bash
docker pull ghcr.io/openzot/openzot:latest

docker run --rm -it \
  --user "$(id -u):$(id -g)" \
  --env HOME=/tmp \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest "add a /health endpoint and a test for it"
```

`--user` makes the files the agent writes belong to you rather than the image's
`zot` user (uid 10001), and `HOME=/tmp` gives that user a writable home so
tools like `git` can store their own config. Both are only needed for host bind
mounts; with a named volume the defaults are fine.

The entrypoint **is** `zot`, so everything after the image name is zot's own
viewer; without a TTY it streams plain text, which is what you want in CI:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" --env HOME=/tmp \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest --max-iterations 40 --task-file TASK.md | tee run.log
```

A container is also the cleanest answer to [Safety](#️-safety): file writes and
shell commands are confined to the volume you mounted, so a run cannot reach the
rest of your machine. See [docs/docker.md](docs/docker.md) for credentials,
config and `AGENT.md` mounts, and hardening flags.

## Backends

zot ships with a backend for each common provider. Pick one with `--backend`, or
set `default_backend` in config.

| Backend      | Endpoint                             | Credential from       |
| ------------ | ------------------------------------ | --------------------- |
| `openai`     | `https://api.openai.com/v1`          | `OPENAI_API_KEY`      |
| `anthropic`  | `https://api.anthropic.com/v1`       | `ANTHROPIC_API_KEY`   |
| `groq`       | `https://api.groq.com/openai/v1`     | `GROQ_API_KEY`        |
| `mistral`    | `https://api.mistral.ai/v1`          | `MISTRAL_API_KEY`     |
| `deepseek`   | `https://api.deepseek.com/v1`        | `DEEPSEEK_API_KEY`    |
| `openrouter` | `https://openrouter.ai/api/v1`       | `OPENROUTER_API_KEY`  |
| `together`   | `https://api.together.xyz/v1`        | `TOGETHER_API_KEY`    |
| `cerebras`   | `https://api.cerebras.ai/v1`         | `CEREBRAS_API_KEY`    |
| `xai`        | `https://api.x.ai/v1`                | `XAI_API_KEY`         |
| `moonshot`   | `https://api.moonshot.cn/v1`         | `MOONSHOT_API_KEY`    |
| `zai`        | `https://api.z.ai/api/paas/v4`       | `ZAI_API_KEY`         |
| `qwen`       | DashScope compatible mode            | `DASHSCOPE_API_KEY`   |
| `ollama`     | `http://localhost:11434/v1`          | none                  |

### Any other provider

Anything that speaks the OpenAI chat-completions API works. Name a backend,
give it a base URL and a key:

```yaml
default_backend: mygateway

backends:
  mygateway:
    provider: custom
    base_url: https://gateway.internal.example.com/v1
    api_key: '$GATEWAY_KEY'
```

The endpoint must be `https` unless it is loopback, and a custom endpoint needs
its own key - a credential is scoped to the host it was issued for, and zot will
not forward one to a URL you just typed.

### The Responses API

On OpenAI, reasoning models use the
[Responses API](https://platform.openai.com/docs/api-reference/responses)
automatically. It carries reasoning state between tool rounds as an opaque item
the model resumes from; chat-completions has nowhere to put it, so a reasoning
model driven that way re-derives its thinking on every round.

Only OpenAI implements it today, so everywhere else stays on chat-completions.
Override per backend if a gateway supports it:

```yaml
backends:
  mygateway:
    provider: custom
    base_url: https://gateway.example.com/v1
    use_responses: true       # or disable_responses: true
```

## Configuration

Configuration is layered: built-in defaults < config file < `ZOT_*` environment
variables < CLI flags. The config file is optional - env vars alone are enough.

```bash
zot config        # opens the config in $EDITOR, creating it from a template
zot config path   # print the config file location
```

Scalar fields have a matching `ZOT_<PATH>` env var (e.g. `agent.model` →
`ZOT_AGENT_MODEL`, `default_backend` → `ZOT_DEFAULT_BACKEND`). Provider
credentials come from their own conventional variables (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, …) and so need no `ZOT_` prefix; they can equally be set in
config, referencing a variable you export. See
[configs/zot.example.yaml](configs/zot.example.yaml).

### Flags

`zot --help` lists them all. The ones worth knowing:

| Flag | Effect |
| ---- | ------ |
| `--backend` / `--model` | which provider and model to run against |
| `--dir` | the directory the agent reads, writes and runs commands in |
| `--max-iterations` | cap the agentic rounds; the default is deliberately large |
| `--task-file` | read the brief from a file instead of the command line |
| `--resume` / `--session-dir` / `--no-session` | see [Sessions](#sessions) |
| `--diff` | show a syntax-highlighted diff under each write |
| `--plain` | stream unstyled output; auto-enabled when stdout is not a terminal |
| `--config` | use a specific config file |

### Controls

Because the agent is autonomous, the only keys are for viewing:

| Key           | Action             |
| ------------- | ------------------ |
| `↑` / `↓`     | scroll the log     |
| `PgUp`/`PgDn` | page the log       |
| `g` / `G`     | jump to top/bottom |
| `q`           | quit               |

## Sessions

Every run is written to `~/.local/state/zot/sessions/` as it happens - one JSON
object per line, holding the brief, the model, every message, every tool call
and how it ended.

That matters because an autonomous run is unattended by definition: nobody
watched it, and by the time you look the terminal is gone. The log is what turns
"it failed overnight" into something you can answer.

```bash
# what has run, newest first
zot sessions

# read one
cat ~/.local/state/zot/sessions/20260805-155859.jsonl | jq .

# pick up where it stopped - the agent keeps everything it already knew
zot --resume last "now add the tests you skipped"

# or continue the original brief, unchanged
zot --resume last
```

`--resume` takes a session id, a path, or `last`. A resumed run writes its own
log and records which session it continued, so a chain of continued runs stays
reconstructable.

The log is appended and flushed line by line, so it is readable while the run is
still going and a killed run still leaves everything up to the kill. Use
`--no-session` to record nothing, or `--session-dir` (or `ZOT_SESSION_DIR`) to
put the logs somewhere else.

## Project context (`AGENT.md` & skills)

On startup zot folds in context from two places - the **config directory**
(`~/.config/zot/`, global) and the **working directory** (`--dir`, per-project):

- **`AGENT.md`** - at the **root** of either directory; its contents are
  appended to the agent's backstory (config first, then project). Use it for
  conventions the agent should always follow.
- **skills** - each `<name>/SKILL.md` (with `name` / `description` YAML front
  matter) is described to the agent in its backstory, and the agent reads a
  skill's full file on demand when it's relevant. Both
  **`.skills/`** (typical at a project root) and **`skills/`** are searched.

```
~/.config/zot/          ./ (your project, --dir)
├── AGENT.md            ├── AGENT.md
└── skills/             └── .skills/
    └── greet/              └── deploy/
        └── SKILL.md            └── SKILL.md
```

Everything here is optional - missing files and directories are ignored.

## ⚠️ Safety

`zot` is fully autonomous and has **real** file-write and shell-exec access
from `--dir`. The flag changes the process working directory; it is **not a
filesystem sandbox**. Absolute paths and shell commands retain all permissions
of the zot process. Point it at a scratch directory or a disposable git checkout
you are happy for it to change - not your home directory.

Configured backend credentials are resolved into zot's in-memory configuration
and then removed from the process environment before the agent starts, so its
shell commands do not inherit those API keys. Other secrets already present in
the environment or readable from disk remain accessible to those commands.

opens a session in, and the prompts come from whoever that client lets through -
so the blast radius is the client's access-control policy, not just yours.

The published [container image](#docker) is the practical way to bound this: the
agent can only touch the volume you mounted, and `docker run` gives you the rest
of the levers (read-only root, dropped capabilities, resource limits) in one
place. [docs/docker.md](docs/docker.md) covers them.

## Architecture

| Path                | Responsibility                                                           |
| ------------------- | ------------------------------------------------------------------------ |
| `cmd/zot/`          | the binary: flag parsing, sessions, working dir, then `zot.Run` |
| `zot.go`            | embeddable core: builds the provider client + agent options and runs it  |
| `agent/`            | the public harness: `ExecuteWithTools`, tools, skills, events            |
| `internal/loop/`    | the agentic loop: budgets, guards, settle mode, message hygiene          |
| `internal/thread/`  | context-window assembly and the four loop-detection heuristics           |
| `internal/compaction/` | condensing older history into a summary when the window fills         |
| `internal/provider/` | the `Transport` seam: chat-completions and the Responses API            |
| `internal/catalogue/` | what each model's context window and capabilities are                  |
| `internal/tokenizer/` | BPE token counting with embedded vocabularies                          |
| `internal/session/` | JSONL run logs: write, read, list, resume                                |
| `internal/config/`  | layered config (defaults < file < env), XDG paths, env overrides         |
| `internal/build/`   | release vs developer build, and what that changes                        |
| `internal/version/` | build-time version stamping and GitHub update checks                     |
| `internal/tui/`     | the Bubble Tea read-only viewer (model, render, styles, agent bridge)    |
| `configs/`          | example configuration                                                    |

Releasing is driven by the `VERSION` file and the GitHub workflows - see
[RELEASES.md](RELEASES.md) and [CHANGELOG.md](CHANGELOG.md).

## Ecosystem

| Project                                       | Role                                                           |
| --------------------------------------------- | -------------------------------------------------------------- |
| [Pantalk](https://github.com/pantalk/pantalk) | Connect coding agents to the chat platforms people already use |
| [MCPShim](https://github.com/mcpshim/mcpshim) | Turn MCP servers and HTTP APIs into standard CLI commands      |
| [crmkit](https://github.com/crmkit/crmkit)    | Give agents a shared CRM and system of record over HTTP or MCP |
