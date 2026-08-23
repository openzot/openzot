# Configuration, flags, sessions & project context

How to configure a run, the flags and keys, where the session logs live, and how
zot picks up `AGENTS.md` and skills. For choosing and configuring a provider, see
[providers.md](providers.md); for work orders, [orders.md](orders.md); for the
agent itself, [how-it-works.md](how-it-works.md).

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
| `--provider` / `--model`                       | which provider and model to run against                           |
| `--dir`                                       | the directory the agent reads, writes and runs commands in; also accepted by `zot new`, which scaffolds the order under `<dir>/.zot/orders` (and drafts there with `--draft`) |
| `--max-iterations`                            | cap the agentic rounds; the default is deliberately large          |
| `--resume` / `--session-dir` / `--no-session` | see [Sessions](#sessions)                                          |
| `--watch`                                     | keep running and watch a folder or glob for new orders - see [Watch mode](#watch-mode) |
| `--records-dir`                               | where run records are written; defaults to `<dir>/.zot/records` - see [The book](#the-book) |
| `--orders-dir`                                | where this project's orders live: what a bare `zot` runs, what a bare `--watch` watches, and where `zot new` files one. Defaults to `<dir>/.zot/orders` |
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

## The header stats

The bar under the title shows how the run is going. Each field is a segment,
shown only when it fits whole - a narrow terminal drops the tail rather than
clipping a number mid-digit, so **order matters**: what you list first is what
survives.

| Stat                  | Shows                                                          |
| --------------------- | -------------------------------------------------------------- |
| `provider` / `model`  | what the run is talking to                                     |
| `task`                | plan steps done, from the agent's own `plan`/`progress` calls   |
| `order`               | position in a batch - `2/5` when running several orders         |
| `iter`                | agentic rounds, against the cap when one is set                 |
| `edits`               | files written or edited                                        |
| `elapsed`             | wall time, against `max_time` when set                          |
| `tps`                 | output tokens per second - is the provider keeping up           |
| `pace`                | average wall time per iteration - what predicts the finish      |
| `tokens`              | provider-reported usage, in and out                             |
| `dir`                 | the working directory, shortened from the left (`…/repos/zot/tool`) |
| `tools`               | cumulative tool calls (off by default)                          |

Set your own list and order with `ui.stats`:

```yaml
ui:
  stats: [task, order, elapsed, tps, model]
```

The defaults are everything above except `tools`, which is a cumulative count
that climbs on every run and says nothing about whether this one is going well.
`dir` is last because it is the longest field and never changes - a static path
is not worth the live stats it would push off the end. A stat with nothing to
report yet (`tps` before the first tokens, `task` before the agent has planned,
`order` outside a batch) shows `-` rather than a confident zero.

## The book

A bare `zot`, run in a project, carries out that project's outstanding work:

```bash
zot new "add rate limiting to the API, with tests"
# edit the acceptance criteria, then:
zot
```

That is the whole loop. Everything below is what it means.

A project's work orders and the record of what has been run from them live
together under one directory at its root:

```
<project>/.zot/
    orders/<slug>.yaml            written by zot new
    records/<slug>/<run-id>.yaml  written by a successful run
```

One dotted directory, the way every other tool that keeps state in a repository
does it. Two top-level `orders/` and `records/` folders claimed generic names in
the root of somebody else's project, which is not zot's to take.

The two halves are not symmetric, and only one of them is a location zot
chooses:

- **Orders are read from anywhere.** An order is advisory input - what to do -
  and may live wherever it is useful: in the repository being worked on, in a
  shared folder of briefs, in a file some other process wrote. `zot <any
  path>/order.yaml` runs exactly that, no book required. `.zot/orders/` is
  where zot looks when you name nothing, and where `zot new` files one when you
  have not said otherwise; `--orders-dir` moves both (`zot new --orders-dir
  ~/briefs`), while `--dir` still says which project the order is *for* (and
  which tree a `--draft` survey reads).
- **Records are written where you point them.** `--records-dir` sets the ledger
  root, and it defaults to `<dir>/.zot/records` - the book of the project being
  worked on, not a directory beside whatever order file was named. The receipt
  belongs with the work, not with the brief.

```bash
# file the brief in a shared folder, but draft it against this project
zot new --draft --dir ~/work/api --orders-dir ~/briefs "add rate limiting"

# the default: the brief is somebody else's, the receipt is this project's
zot --dir ~/work/api ~/briefs/add-rate-limiting.yaml

# one ledger for a fleet of projects
zot --records-dir ~/zot-ledger ~/briefs/*.yaml
```

An order may declare an optional `title:` - a short label for people, shown in
the viewer instead of the order text. Without one the file name is used, its
dashes read as spaces (`fix-the-flaky-test.yaml` becomes "Fix the flaky test"),
so most orders need not set it. The title never reaches the agent: the objective
is the contract, and a title is only how you recognise it on screen. `zot new
--draft` asks the model to propose one.

Doneness is derived from that ledger and never stored on the order: a settled
record of the order's exact content means the order is skipped next time, and
editing the order changes its hash and re-queues it. That is what makes a bare
`zot` re-runnable - it runs the whole book every time and the ledger decides
what is still outstanding, so there is no queue to drain and nothing to clean
up. Because the ledger follows the project, the same order run against two
different projects is two separate pieces of work with two separate histories -
which is what it is.

## Watch mode

`--watch` turns a one-shot invocation into a standing one: instead of running
the batch and exiting, zot stays up and runs every `*.yaml` work order that
shows up in the watched target - including orders already sitting there when it
starts - as it arrives. This is how zot becomes a drop-box factory: write an
order, and the running watcher picks it up without a restart.

```bash
# watch this project's own orders - the usual case
zot --watch

# a drop box that is not the book at all - orders are read from anywhere
zot --watch ~/inbox

# a glob, if only some of a folder's files are orders
zot --watch "~/inbox/*.yaml"

# watch one project's orders while working from anywhere else; like an order
# path, a named target resolves against the directory you invoke from
zot --dir ~/work/api --watch
```

Each order runs exactly as it would in a batch (`zot .zot/orders/*.yaml`): its
own run, its own session log, its own recorded outcome in the ledger - one at a
time, in filename order. The ledger applies, so an order that
already ran is skipped, and editing an order re-queues it. An order whose last
run did not conclude is continued automatically.

A failed order is reported and the watch goes on - one bad afternoon must not
end the factory - but nothing retries it behind your back: fix or edit the
order and its new content is picked up on the next sweep. The target is swept
every second, so a folder that does not exist yet can be created after the
watch starts. Ctrl-C (or SIGTERM) stops watching and exits.

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

### Exporting sessions

The log is zot's own shape. `zot sessions export` renders sessions as
**trajectories** - the conversation in the chat convention everything else
reads (`system`, `user`, `assistant` with `tool_calls`, `tool`), with the run's
task, model, outcome and timings beside it - which is what you want for
analysis, evaluation or building a training dataset.

```bash
# the last session, as one JSON line on stdout
zot sessions export

# named sessions into a directory: <id>.jsonl each, screenshots under images/
zot sessions export 20260805-155859 20260805-171012 --out ./trajectories

# every finished chain in the session directory
zot sessions export --all --out ./trajectories

# keep the conversations that compaction or a resume superseded, too
zot sessions export --snapshots --out ./trajectories
```

A session that continued earlier ones is exported as **one** trajectory - the
last log carries the whole conversation - and its `chain` lists the sessions
behind it. `messages` is the conversation as it stood at the end; after
compaction that is a summary plus the recent turns, so `--snapshots` adds the
earlier states (oldest first) for anyone who wants every turn that happened.
Each message keeps zot's own `type` beside its `role`, and an assistant turn
carries the model's `reasoning` when the provider surfaced it. Images the model
was shown are copied next to the export and referenced by relative path; on
stdout the turn keeps only its text. The system prompt is not in the log and so
not in the export - `task` is the brief the run was given.

The log is appended and flushed line by line, so it is readable while the run is
still going and a killed run still leaves everything up to the kill. Use
`--no-session` to record nothing, or `--session-dir` (or `ZOT_SESSION_DIR`) to
put the logs somewhere else.

## The update check

A release build asks GitHub for the latest zot release while a run is going and,
if the binary is behind, says so on stderr once the viewer has released the
screen:

```
A new version of zot is available: v0.18.0 → v0.19.0
Upgrade: curl -fsSL https://zot.im/install.sh | bash
Release: https://github.com/openzot/openzot/releases/tag/v0.19.0
```

The installer replaces the binary in place, so that line is the whole upgrade
(`ZOT_VERSION=vX.Y.Z` pins a specific release). The check is silent when the
build is current, silent on any failure - an unreachable GitHub, a rate limit -
and never made at all by a `dev` build.

It is the only request a run makes that is not to your provider, and nothing
about you or the run travels with it. To make no call at all - an air-gapped
host, a locked-down CI job, or a preference that zot speaks to nothing but the
provider - turn it off:

```yaml
update_check:
  disabled: true
```

or `ZOT_UPDATE_CHECK_DISABLED=true`.

## Project context (`AGENTS.md` & skills)

On startup zot folds in context from two places - the **config directory**
(`~/.config/zot/`, global) and the **working directory** (`--dir`, per-project):

- **`AGENTS.md`** - at the **root** of either directory; its contents are
  appended to the agent's instructions (config first, then project). Use it for
  conventions the agent should always follow.
- **skills** - each `<name>/SKILL.md` (with `name` / `description` YAML front
  matter) is described to the agent in its instructions, and the agent reads a
  skill's full file on demand when it's relevant. Both
  **`.skills/`** (typical at a project root) and **`skills/`** are searched.

```
~/.config/zot/          ./ (your project, --dir)
├── AGENTS.md           ├── AGENTS.md
└── skills/             └── .skills/
    └── greet/              └── deploy/
        └── SKILL.md            └── SKILL.md
```

Everything here is optional - missing files and directories are ignored.
