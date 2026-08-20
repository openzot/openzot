# Configuration, flags, sessions & project context

How to configure a run, the flags and keys, where the session logs live, and how
zot picks up `AGENT.md` and skills. For choosing and configuring a provider, see
[providers.md](providers.md); for the agent itself, the [README](../README.md).

## The config file and env vars

Configuration is layered: built-in defaults < config file < `ZOT_*` environment
variables < CLI flags. The config file is optional - env vars alone are enough.

```bash
zot config        # opens the config in $EDITOR, creating it from a template
zot config path   # print the config file location
```

Scalar fields have a matching `ZOT_<PATH>` env var (e.g. `agent.model` →
`ZOT_AGENT_MODEL`, `default_provider` → `ZOT_DEFAULT_PROVIDER`). Provider
credentials come from their own conventional variables (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, …) and so need no `ZOT_` prefix; they can equally be set in
config, referencing a variable you export. Every field is documented in
[configs/zot.example.yaml](../configs/zot.example.yaml).

## Flags

`zot --help` lists them all. The ones worth knowing:

| Flag                                          | Effect                                                             |
| --------------------------------------------- | ------------------------------------------------------------------ |
| `--provider` / `--model`                       | which provider and model to run against                            |
| `--dir`                                       | the directory the agent reads, writes and runs commands in         |
| `--max-iterations`                            | cap the agentic rounds; the default is deliberately large          |
| `--resume` / `--session-dir` / `--no-session` | see [Sessions](#sessions)                                          |
| `--diff`                                      | show a syntax-highlighted diff under each write                    |
| `--plain`                                     | stream unstyled output; auto-enabled when stdout is not a terminal |
| `--config`                                    | use a specific config file                                         |

## Controls

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

# pick up where it stopped - the agent keeps everything it already knew, and
# continues the order the session was started with
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
  appended to the agent's instructions (config first, then project). Use it for
  conventions the agent should always follow.
- **skills** - each `<name>/SKILL.md` (with `name` / `description` YAML front
  matter) is described to the agent in its instructions, and the agent reads a
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
