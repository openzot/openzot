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

The factory is powered by an autonomous coding harness. It runs the whole loop
(plan → act → observe → verify → exit) from one brief, without waiting for
follow-up prompts at every step. Model calls, tool orchestration, planning, and
iteration run on a capable cloud harness, not in the binary. zot defaults to the
built-in `relay` backend, which reaches models through the CBK Relay with your
own provider key (OpenAI, Mistral, …); the `cbk` / `chatbotkit` backends use a
[ChatBotKit](https://chatbotkit.com) token instead. That keeps the local runtime
tiny: load config, wire the SDK's tools, and render events.

## The factory model

| Stage  | What zot does                                                    |
| ------ | ---------------------------------------------------------------- |
| Brief  | Accepts one task plus the repository's `AGENT.md` and skills     |
| Plan   | Breaks the requested outcome into executable work                |
| Build  | Reads, writes, and edits files, then runs commands in the repo   |
| Verify | Inspects results and iterates when the job is not complete       |
| Output | Leaves the completed work in the working tree with a visible log |

## How it works

The automated coding harness comes from the
[**ChatBotKit Go SDK**](https://github.com/chatbotkit/go-sdk):

- `agent.ExecuteWithTools` runs the model in a loop - _plan → act → observe →
  progress → exit_ - until it decides the task is done or it hits the iteration
  cap.
- `agent.DefaultTools()` gives it the coding toolbox: `read`, `write`, `edit`,
  and `exec` (shell).

`zot` itself is a [Bubble Tea](https://github.com/charmbracelet/bubbletea)
front-end for that production run. It launches the harness in a goroutine and
renders the event stream (`ToolCallStart`, `ToolCallEnd`, `Iteration`, token
narration, `AgentExit`, …) into a scrollable, read-only viewport. The UI
deliberately has no text input.

## Prerequisites

- A credential for the selected backend: a provider key for the default `relay`
  (`RELAY_API_KEY`), or a ChatBotKit token for `cbk` / `chatbotkit`
- Go 1.25+ - only if you build from source

## Backend credentials

zot defaults to the **`relay`** backend, which carries *your own provider key*
per model - see [Backends](#backends) for how relay auth is composed into the
model string. Point a provider key at it with `RELAY_API_KEY` (or per-model
`authorization` in config):

```bash
export RELAY_API_KEY="sk-…"   # your OpenAI / provider key
```

The **`cbk`** and **`chatbotkit`** backends instead need a ChatBotKit API token.
**Mint a new one for the tool** - don't reuse a token from elsewhere. We
**recommend** creating a scoped token at
[chatbotkit.com/apps/code](https://chatbotkit.com/apps/code). This issues a token
limited to coding-harness operations only, so it **cannot** reach the rest of
your account.

Provide the token either way:

**1. Environment variable (preferred)** - export `CBK_API_SECRET` (for `cbk`) or
`CHATBOTKIT_API_SECRET` (for `chatbotkit`), or put it in a `.env` file in the
working directory:

```bash
export CBK_API_SECRET="cbk_…"
```

**2. Config file** - set `api_secret` under the backend in your config file
(`~/.config/zot/config.yaml`, or the path given to `--config`):

```yaml
# ~/.config/zot/config.yaml
backends:
  cbk:
    api_secret: 'cbk_…'
```

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

Requires Go 1.25+.

```bash
git clone https://github.com/openzot/openzot
cd openzot
make build               # or: go build -o zot ./cmd/zot
./zot --version
```

`make build` stamps the version into the binary; `make test`, `make vet`, and
`make cross GOOS=… GOARCH=…` are also available.

### Docker

Official Linux amd64 and arm64 images are published to GitHub Container
Registry from the same version tag as the binary release. The agent works in
`/workspace` - mount the checkout you are happy for it to change there:

```bash
docker pull ghcr.io/openzot/openzot:latest

docker run --rm -it \
  --user "$(id -u):$(id -g)" \
  --env HOME=/tmp \
  --env CHATBOTKIT_API_SECRET \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest "add a /health endpoint and a test for it"
```

`--user` makes the files the agent writes belong to you rather than the image's
`zot` user (uid 10001), and `HOME=/tmp` gives that user a writable home so
tools like `git` can store their own config. Both are only needed for host bind
mounts; with a named volume the defaults are fine.

The entrypoint **is** `zot`, so everything after the image name is zot's own
arguments (`--version`, `--diff`, `acp`, …). With `-it` you get the full-screen
viewer; without a TTY it streams plain text, which is what you want in CI:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" --env HOME=/tmp \
  --env CHATBOTKIT_API_SECRET \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest --max-iterations 40 --task-file TASK.md | tee run.log
```

A container is also the cleanest answer to [Safety](#️-safety): file writes and
shell commands are confined to the volume you mounted, so a run cannot reach the
rest of your machine. See [docs/docker.md](docs/docker.md) for credentials,
config and `AGENT.md` mounts, ACP mode, and hardening flags.

## Usage

```bash
export CHATBOTKIT_API_SECRET="your-api-key"   # or use .env

# run it on a task
./zot "add input validation to the signup handler and a test"

# use a scratch directory as the working directory and cap the work
./zot --dir ./scratch --max-iterations 40 "scaffold a tiny snake game in python"

# read the task from a file instead of the command line
./zot --task-file TASK.md

# serve the agent to an ACP client instead of running a task
./zot acp
```

### Flags

| Flag               | Default                     | Description                                                |
| ------------------ | --------------------------- | ---------------------------------------------------------- |
| `--model`          | `kimi-k2.7-code`            | Model name (resolved against the selected backend)         |
| `--backend`        | `relay`                     | Backend to run against: `relay`, `cbk`, or `chatbotkit`    |
| `--dir`            | `.`                         | Working directory the agent reads, writes and runs in      |
| `--max-iterations` | `1000`                      | Safety cap before the agent is forced to stop              |
| `--task-file`      | _(none)_                    | Read the task from a file instead of the command line      |
| `--diff`           | `false`                     | Show a syntax-highlighted diff panel under each edit/write |
| `--plain`          | `false`                     | Stream unstyled output (auto-enabled when not a TTY)       |
| `--feature`        | _(none)_                    | Enable a feature by name (repeatable): `web`, `chunking`   |
| `--config`         | `~/.config/zot/config.yaml` | Path to a config file (optional)                           |
| `--version`        |                             | Print the version and exit                                 |

### Diffs

With `--diff` (or `ui.diff: true`, or `ZOT_UI_DIFF=true`), every `edit`/`write`
is followed by a framed, syntax-highlighted before/after panel rendered inline in
the activity log - scroll back to review any change the agent made:

```
  edit   internal/server/server.go
 ╭───────────────────────────────────────────────────────────╮
 │ internal/server/server.go  +2 -1                          │
 │   func (s *Server) routes() {                             │
 │ -   mux.HandleFunc("/", s.handleIndex)                    │
 │ +   mux.HandleFunc("/", s.handleIndex)                    │
 │ +   mux.HandleFunc("/health", s.handleHealth)             │
 │   }                                                       │
 ╰───────────────────────────────────────────────────────────╯
```

Highlighting is powered by [chroma](https://github.com/alecthomas/chroma); the
panel shows ±3 lines of context and caps very large rewrites.

### Non-interactive (plain) mode

The full-screen viewer needs a terminal. When stdout is **not** a TTY - piped,
redirected, run from CI, or driven by another program - zot automatically falls
back to **plain mode**: it streams the same activity as unstyled text lines
(`--diff` still works, rendered as a plain unified diff) instead of starting an
alt-screen UI that would garble or fail. Force it in a terminal with `--plain`
(or `ui.plain: true` / `ZOT_UI_PLAIN=true`):

```bash
zot --plain "tidy go.mod" | tee run.log
```

### Features

Enable ChatBotKit conversation features for the run - each a name/options pair.
Currently exposed: **`web`** (live web `search`/`fetch`) and **`chunking`**. Set
them with repeated `--feature` flags:

```bash
zot --feature web --feature chunking "research the latest go release and summarise it"
```

…or in the config file, where you can also pass per-feature options:

```yaml
features:
  - name: web
    options:
      search: true
      fetch: true
  - name: chunking
```

`--feature` flags replace the configured list when given. (The list isn't
settable via a single env var - use the config file for options.)

## ACP mode (`zot acp`)

`zot acp` serves the same agent over the
[**Agent Client Protocol**](https://agentclientprotocol.com/), so an ACP client -
an editor, or an agent harness - can drive zot instead of you:

```bash
zot acp
```

The protocol is JSON-RPC on stdin/stdout, so this is meant to be **spawned by a
client**, not run by hand. Everything else about zot is unchanged: the same
agent loop, the same `read`/`write`/`edit`/`exec` tools, the same config and
backends. What changes is the shape of a run:

|                   | normal run                   | `zot acp`                                    |
| ----------------- | ---------------------------- | -------------------------------------------- |
| Work comes from   | the task on the command line | `session/prompt`, turn by turn               |
| Working directory | `--dir`                      | the `cwd` the client opens each session with |
| Output            | the read-only viewer         | `session/update` notifications               |
| Lifetime          | one task, then exit          | stays up until the client disconnects        |

Each session keeps its own conversation history, so follow-up prompts continue
where the last turn left off, and `AGENT.md` / skills are loaded **per session**
from the directory the client supplies - a client working across several
repositories gets the right context in each.

The mode takes `--config`, `--backend`, `--model`, `--max-iterations` and
`--feature`. It takes no task and no `--dir`, and the viewer flags don't apply.
Diagnostics go to stderr; stdout belongs to the protocol.

### Connecting to Buzz

[Buzz](https://github.com/block/buzz) is a self-hostable workspace where humans
and agents share channels. Its `buzz-acp` harness spawns any ACP agent, so
pointing it at zot is two environment variables:

```bash
export BUZZ_ACP_AGENT_COMMAND=zot
export BUZZ_ACP_AGENT_ARGS=acp
export CHATBOTKIT_API_SECRET="your-api-key"

buzz-acp
```

Buzz's own operating instructions arrive with each prompt, and zot's `exec` tool
runs the `buzz` CLI to post replies, open pull requests, and so on. Note that
zot has **no MCP client**: any MCP servers the harness offers are logged and
ignored, which costs nothing by default (`BUZZ_ACP_MCP_COMMAND` is empty).

> ⚠️ An ACP-mode zot is an unattended `exec` on the machine that runs it,
> reachable by whoever the client lets through. Buzz defaults to
> `--respond-to owner-only`; keep it that way unless you mean otherwise, and see
> [Safety](#️-safety).

### Limits

- **One turn at a time.** The SDK's file and shell tools resolve paths against
  the process working directory, so a turn owns it. Clients that drive one
  prompt at a time - `buzz-acp` does - never notice; a client wanting parallel
  sessions should spawn a process per workspace.
- **No permission prompts.** zot is autonomous by design: it does not call
  `session/request_permission`, so the client sees tool calls as they happen
  rather than being asked to approve them.
- **No `session/load`.** History lives in the process; restarting an agent
  starts its sessions fresh.

## Backends

A run targets a **backend** - the provider zot talks to. Three ship built in:

| Backend      | Endpoint                     | Auth style | Credential              |
| ------------ | ---------------------------- | ---------- | ----------------------- |
| `relay`      | `https://relay.cbk.ai`       | per-model  | provider key per model  |
| `cbk`        | `https://api.cbk.ai`         | Bearer     | `CBK_API_SECRET`        |
| `chatbotkit` | `https://api.chatbotkit.com` | Bearer     | `CHATBOTKIT_API_SECRET` |

zot defaults to **`relay`**. Pick another with `--backend` (or `default_backend`
in config). The model is resolved against the chosen backend, so it must be one
that backend serves.

The two ChatBotKit backends authenticate with a Bearer token. The **relay is
different**: it authenticates each model with *its own provider key*, carried
inside the model string as `<model>/authorization=<key>` - because on the relay
each model is a different provider (OpenAI, Mistral, …) with a different key. So
relay auth is configured **per model**:

```yaml
default_backend: relay
backends:
  relay:
    # authorization: $RELAY_API_KEY   # optional: one default key for all models
    models:
      gpt-4:
        authorization: $OPENAI_API_KEY
      mistral-large:
        authorization: $MISTRAL_API_KEY
```

```bash
export OPENAI_API_KEY="sk-..."
zot --model gpt-4 "…"            # → sends model gpt-4/authorization=sk-... to the relay

# A single default key for every relay model (composed the same way):
export RELAY_API_KEY="sk-..."
zot --model gpt-4 "…"

# You can also inline the key yourself; zot leaves it untouched:
zot --model 'gpt-4/authorization=sk-...' "…"

# The ChatBotKit backends just need their Bearer token:
export CBK_API_SECRET="..."
zot --backend cbk --model kimi-k2.7-code "…"
```

zot composes the `authorization=` param onto the model automatically from the
per-model (or backend-level) config; a key you inline into `--model` yourself is
left as-is.

Each backend can also define **custom models** in the config; when `--model`
matches a key, that entry's settings take priority (alias the real id, cap
iterations, add features, and - on the relay - carry that model's provider key):

```yaml
default_backend: relay
backends:
  relay:
    models:
      fast:
        model: gpt-4o-mini
        authorization: $OPENAI_API_KEY
        max_iterations: 50
  cbk:
    # api_secret: '$CBK_API_SECRET'   # default
    models:
      cheap:
        model: kimi-k2.7-code
        max_iterations: 50
```

```bash
zot --model fast "…"   # uses the relay's "fast" model config
```

## Configuration

Configuration is layered: built-in defaults < config file < `ZOT_*` environment
variables < CLI flags. The config file is optional - env vars alone are enough.

```bash
mkdir -p ~/.config/zot
cp configs/zot.example.yaml ~/.config/zot/config.yaml
```

Scalar fields have a matching `ZOT_<PATH>` env var (e.g. `agent.model` →
`ZOT_AGENT_MODEL`, `default_backend` → `ZOT_DEFAULT_BACKEND`). Backend
credentials come from their own env vars (`RELAY_API_KEY` for `relay`,
`CBK_API_SECRET` for `cbk`, `CHATBOTKIT_API_SECRET` for `chatbotkit`), so they
don't need the `ZOT_` prefix. See [configs/zot.example.yaml](configs/zot.example.yaml).

### Controls

Because the agent is autonomous, the only keys are for viewing:

| Key           | Action             |
| ------------- | ------------------ |
| `↑` / `↓`     | scroll the log     |
| `PgUp`/`PgDn` | page the log       |
| `g` / `G`     | jump to top/bottom |
| `q`           | quit               |

## Project context (`AGENT.md` & skills)

On startup zot folds in context from two places - the **config directory**
(`~/.config/zot/`, global) and the **working directory** (`--dir`, per-project):

- **`AGENT.md`** - at the **root** of either directory; its contents are
  appended to the agent's backstory (config first, then project). Use it for
  conventions the agent should always follow.
- **skills** - each `<name>/SKILL.md` (with `name` / `description` YAML front
  matter) is loaded via the SDK and passed to the agent as a `skills` feature;
  the agent reads a skill's full file on demand when it's relevant. Both
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

In [ACP mode](#acp-mode-zot-acp) the same applies to every directory a client
opens a session in, and the prompts come from whoever that client lets through -
so the blast radius is the client's access-control policy, not just yours.

The published [container image](#docker) is the practical way to bound this: the
agent can only touch the volume you mounted, and `docker run` gives you the rest
of the levers (read-only root, dropped capabilities, resource limits) in one
place. [docs/docker.md](docs/docker.md) covers them.

## Architecture

| Path                | Responsibility                                                           |
| ------------------- | ------------------------------------------------------------------------ |
| `cmd/zot/`          | the binary: flag parsing, `.env`, working dir, then `zot.Run`/`ServeACP` |
| `zot.go`            | embeddable core: builds the SDK client + agent options and runs it       |
| `internal/config/`  | layered config (defaults < file < env), XDG paths, env overrides         |
| `internal/version/` | build-time version stamping and GitHub update checks                     |
| `internal/tui/`     | the Bubble Tea read-only viewer (model, render, styles, agent bridge)    |
| `internal/acp/`     | the Agent Client Protocol server: sessions, turns, event bridge          |
| `configs/`          | example configuration                                                    |

Releasing is driven by the `VERSION` file and the GitHub workflows - see
[RELEASES.md](RELEASES.md) and [CHANGELOG.md](CHANGELOG.md).

## Ecosystem

| Project                                       | Role                                                           |
| --------------------------------------------- | -------------------------------------------------------------- |
| [Pantalk](https://github.com/pantalk/pantalk) | Connect coding agents to the chat platforms people already use |
| [MCPShim](https://github.com/mcpshim/mcpshim) | Turn MCP servers and HTTP APIs into standard CLI commands      |
| [crmkit](https://github.com/crmkit/crmkit)    | Give agents a shared CRM and system of record over HTTP or MCP |
