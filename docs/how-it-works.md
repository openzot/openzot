# How zot works

The engine, the parts under it worth knowing about, and how zot thinks about
sub-agents. For running zot, see the [README](../README.md); for work orders,
[orders.md](orders.md).

## The harness

The harness is zot's own, in the [`agent`](../agent) package:

- `agent.ExecuteWithTools` runs the model in a loop - it calls tools, sees the
  results, and goes again - until it records an outcome (`success` / `failure`)
  or a budget or guard stops it.
- `agent.DefaultTools()` gives it the toolbox: `read`, `write`, `list` and
  `shell` for the work, plus `plan` and `progress` to structure and narrate it.
  `agent.DefaultToolsFor(agent.ToolOptions{Vision: true})` adds `view`, which
  shows the model an image file rather than describing one - offered only to a
  model that can actually see, which zot takes from its catalogue and the
  operator's config.

That package is importable, so the same engine drives more than coding:
[Rook](https://github.com/pdparchitect/rook), an AI bug-hunting and security-audit
agent, is built on it with its own toolset and skills.

## Under the harness

- **Thread assembly** fits the conversation to the model's context window,
  newest-first, keeping tool calls paired with their results.
- **Context strategy** decides what happens as that window fills. `compact` (the
  default) summarises the older history into a checkpoint with a model call, so a
  long run keeps a condensed memory of its early turns instead of losing them;
  `truncate` simply drops the oldest messages to fit. A checkpoint is preserved
  verbatim and never re-summarised, and an outright provider rejection falls back
  to a no-model summary and retries. Configurable - see
  [configs/zot.example.yaml](../configs/zot.example.yaml).
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
  `--resume` (see [configuration.md](configuration.md#sessions)).

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
  instructions in your `AGENTS.md`, or a skill, are what tell it when that is
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
engine. Orchestration is content, not framework. See
[project context](configuration.md#project-context-agentsmd--skills).
