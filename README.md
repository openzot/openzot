# zot

An autonomous coding agent you **watch**, not drive.

`zot` inverts the usual coding-TUI interaction model. There is **no prompt and no
chat box**. You hand it a single task on the command line, and it works the
problem entirely on
its own — reading files, editing them, and running shell commands — while the
terminal streams a live, **read-only** view of every step it takes.

```
✦ zot  ⠹ working   add a /health endpoint and a test for it
model kimi-k2.7-code  ·  dir ~/scratch/api  ·  iter 3  ·  tools 9  ·  edits 2  ·  elapsed 00:41

── iteration 1 ───────────────────────────────────────────────────────────────
  plan  break the task into endpoint + test, then verify
    1. read the existing router
    2. add the /health handler
    3. write a test and run it
  read   server.go
    ✓ 84 lines
── iteration 2 ───────────────────────────────────────────────────────────────
  edit   server.go
    ✓ applied
  write  server_test.go
    ✓ saved
  exec   go test ./...
    │ ok  	example/api	0.012s
    ✓ done
  ▸ all steps complete, tests green

✓ done  added /health endpoint and a passing test

↑/↓ scroll  g/G top/bottom  q quit
```

## How it works

All of the autonomy comes from the **ChatBotKit Go SDK** (`../../../sdks/go`):

- [`agent.ExecuteWithTools`](../../../sdks/go/agent/agent.go) runs the model in a
  loop — _plan → act → observe → progress → exit_ — until it decides the task is
  done or it hits the iteration cap.
- [`agent.DefaultTools()`](../../../sdks/go/agent/tools.go) gives it the coding
  toolbox: `read`, `write`, `edit`, and `exec` (shell).

`zot` itself is just a [Bubble Tea](https://github.com/charmbracelet/bubbletea)
front-end. It launches the agent in a goroutine and renders the event stream
(`ToolCallStart`, `ToolCallEnd`, `Iteration`, token narration, `AgentExit`, …)
into a scrollable, read-only viewport. The UI deliberately has no text input.

## Prerequisites

- Go 1.24+
- A ChatBotKit API key — get one at [chatbotkit.com](https://chatbotkit.com)

## Setup

```bash
cd incubator/zot/tool
cp .env.example .env   # then add your CHATBOTKIT_API_SECRET
make build             # or: go build -o zot ./cmd/zot
```

`make build` stamps the version into the binary; `make test`, `make vet`, and
`make cross GOOS=… GOARCH=…` are also available.

## Usage

```bash
export CHATBOTKIT_API_SECRET="your-api-key"   # or use .env

# run it on a task
./zot "add input validation to the signup handler and a test"

# sandbox it to a scratch directory and cap the work
./zot --dir ./scratch --max-iterations 40 "scaffold a tiny snake game in python"

# read the task from a file instead of the command line
./zot --task-file TASK.md
```

### Flags

| Flag               | Default                     | Description                                                |
| ------------------ | --------------------------- | ---------------------------------------------------------- |
| `--model`          | `kimi-k2.7-code`            | ChatBotKit model alias driving the agent                   |
| `--dir`            | `.`                         | Working directory the agent reads, writes and runs in      |
| `--max-iterations` | `1000`                      | Safety cap before the agent is forced to stop              |
| `--task-file`      | _(none)_                    | Read the task from a file instead of the command line      |
| `--diff`           | `false`                     | Show a syntax-highlighted diff panel under each edit/write |
| `--plain`          | `false`                     | Stream unstyled output (auto-enabled when not a TTY)       |
| `--config`         | `~/.config/zot/config.yaml` | Path to a config file (optional)                           |
| `--version`        |                             | Print the version and exit                                 |

### Diffs

With `--diff` (or `ui.diff: true`, or `ZOT_UI_DIFF=true`), every `edit`/`write`
is followed by a framed, syntax-highlighted before/after panel rendered inline in
the activity log — scroll back to review any change the agent made:

```
  edit   internal/server/server.go
 ╭───────────────────────────────────────────────────────────╮
 │ internal/server/server.go  +2 -1                           │
 │   func (s *Server) routes() {                              │
 │ -   mux.HandleFunc("/", s.handleIndex)                     │
 │ +   mux.HandleFunc("/", s.handleIndex)                     │
 │ +   mux.HandleFunc("/health", s.handleHealth)             │
 │   }                                                        │
 ╰───────────────────────────────────────────────────────────╯
```

Highlighting is powered by [chroma](https://github.com/alecthomas/chroma); the
panel shows ±3 lines of context and caps very large rewrites.

### Non-interactive (plain) mode

The full-screen viewer needs a terminal. When stdout is **not** a TTY — piped,
redirected, run from CI, or driven by another program — zot automatically falls
back to **plain mode**: it streams the same activity as unstyled text lines
(`--diff` still works, rendered as a plain unified diff) instead of starting an
alt-screen UI that would garble or fail. Force it in a terminal with `--plain`
(or `ui.plain: true` / `ZOT_UI_PLAIN=true`):

```bash
zot --plain "tidy go.mod" | tee run.log
```

## Configuration

Configuration is layered: built-in defaults < config file < `ZOT_*` environment
variables < CLI flags. The config file is optional — env vars alone are enough.

```bash
mkdir -p ~/.config/zot
cp configs/zot.example.yaml ~/.config/zot/config.yaml
```

Every field has a matching `ZOT_<PATH>` env var (e.g. `agent.model` →
`ZOT_AGENT_MODEL`). The API secret is read from the platform-standard
`CHATBOTKIT_API_SECRET` (endpoint from `CHATBOTKIT_HOST`), so it does not need
the `ZOT_` prefix. See [configs/zot.example.yaml](configs/zot.example.yaml).

### Controls

Because the agent is autonomous, the only keys are for viewing:

| Key           | Action             |
| ------------- | ------------------ |
| `↑` / `↓`     | scroll the log     |
| `PgUp`/`PgDn` | page the log       |
| `g` / `G`     | jump to top/bottom |
| `q`           | quit               |

## ⚠️ Safety

`zot` is fully autonomous and has **real** file-write and shell-exec access
to `--dir`. It will create, modify and delete files and run commands without
asking. Point it at a scratch directory or a disposable git checkout you are
happy for it to change — not your home directory.

## Architecture

| Path                | Responsibility                                                        |
| ------------------- | --------------------------------------------------------------------- |
| `cmd/zot/`          | the binary: flag parsing, `.env`, working dir, then calls `zot.Run`   |
| `zot.go`            | embeddable core: builds the SDK client + agent options and runs it    |
| `internal/config/`  | layered config (defaults < file < env), XDG paths, env overrides      |
| `internal/version/` | build-time version stamping and GitHub update checks                  |
| `internal/tui/`     | the Bubble Tea read-only viewer (model, render, styles, agent bridge) |
| `configs/`          | example configuration                                                 |

Releasing is driven by the `VERSION` file and the GitHub workflows — see
[RELEASES.md](RELEASES.md) and [CHANGELOG.md](CHANGELOG.md).
