# Changelog

All notable changes to zot, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [0.10.0] - unreleased

### Added

- **Local Docker compute for zotui.** A `local` repo mounts an existing checkout into an ephemeral container supplied by `compute.type: docker`; zotui streams the run output and removes the container even after cancellation.
- **Vercel Sandbox compute for zotui.** `compute.type: vercel` creates a non-persistent Vercel Sandbox from a project VCR image, checks out the selected remote Git repository using any credential minted by its repo connection, installs the embedded Zot worker and model configuration privately, streams raw ANSI output, and stops the sandbox after the run. An explicitly listed public GitHub repo can be used without App credentials for a credential-free smoke test.
- **A repository development container for Go and zotui work.** It pins the project toolchain, runs as a non-root developer, provides Git with Git LFS and Docker-in-Docker, persists Go caches, Docker data, and zotui state in named volumes, forwards the command-center port, builds the embedded Linux worker artifacts, and includes a credential-free fixture configuration. `make dev-ui` provides the same local workflow outside the container.
- **GitHub App repository connections for zotui.** An App ID, installation ID, and PEM private key are enough for the command center to discover an installation's repositories and mint a fresh token restricted to the selected repository for each run. The private key stays on the host; remote compute receives only the short-lived clone credential and `GH_TOKEN`.
- **Self-deploying Zot workers.** Zotui embeds release-mode Linux amd64 and arm64 Zot executables, selects the compute platform, and installs the matching binary into each Docker or Vercel sandbox alongside its private runtime configuration. Environment images now provide only toolchains and project dependencies; they no longer need a Zot-specific base image.

### Changed

- **Breaking: model backends are now providers throughout Zot's user-facing surface.** Select one with `--provider`; configure `default_provider` and `providers`; and use `driver` when a named provider connection (such as `corporate`) needs to select a different implementation (such as `openai`). The old `--backend`, `default_backend`, `backends`, and nested `provider` spellings are removed rather than retained as aliases. The TUI stat and session metadata follow the same vocabulary (`provider` for the selected connection, `driver` for the resolved implementation), and ZotUI emits the new worker configuration.
- **Provider-scoped model lists now work consistently across Zot and ZotUI.** A provider with no `models` uses Zot's built-in catalogue; an explicit `models` map replaces it with a custom alias/allowlist. In standalone Zot the list remains optional and an omitted list keeps accepting uncatalogued new models. **Breaking (`zotui` config):** models now live under their provider, environments name both `provider` and `model`, and workers/runs persist that pair. ZotUI requires every enabled provider to resolve to at least one custom or built-in model because the provider-first worker form is driven by that list. ZotUI's file remains independent from standalone Zot's config.
- **Breaking (`zotui`): the terminal command center is replaced by the browser software-factory UI.** Running `zotui` now serves the embedded web app on `127.0.0.1:8080` (override with `ZOTUI_ADDR`). Its backend models durable reusable workers, recurring schedules, and distinct run histories with controls and output, replacing the one-shot `jobs` table. Migration 2 removes that experimental table and its records.
- **Breaking (`zotui` config): repository connections are now `repos`, compute providers are `compute`, and an environment references `compute`.** The configuration now reads as the job path itself: repo + environment + model, with the environment binding compute to its image and variables. The old `sources`, `runners`, and `environments.*.runner` keys are removed rather than retained as aliases; unknown keys now fail during config loading instead of being silently ignored.
- **The CLI now uses GNU-style flags (`spf13/pflag`), matching rook and the rest of the ecosystem.** Long flags are double-dash (`--provider`, `--dir`), flags may appear **after** the positional task (`zot "do X" --plain` now works, where before the flag was folded into the task string), and `--help`'s flag list finally renders `--flag` to match the examples and the docs - the single-/double-dash mismatch the stdlib `flag` package produced is gone. **Breaking:** single-dash long flags (`-provider`) no longer parse - use `--provider`. zot has no short flags and every documented example already used `--`, so anything following the docs is unaffected.

### Fixed

- **The zotui worker form now presents repositories from the selected code source.** A sole configured repository is selected automatically, multiple repositories are offered as a dropdown, and connections without a fixed list retain an explicitly explained `owner/name` field instead of making every repository look like unvalidated free text.
- **The browser command center now has a product route at `/workers`.** `/` redirects there, while the mockup artifact path `/operations/instances.html` is no longer public.
- **New zotui workers now inherit zot's 1,000,000-iteration default.** The browser, application validation, and persistence path no longer carry conflicting 20-iteration scaffold fallbacks.

## [0.9.1] - 2026-08-06

### Added

- **`tui.Meta.AppName` names the embedding application** in the title badge (`✦ rook`), the startup line and the plain-mode header, so a host reads as itself rather than "zot". Empty defaults to "zot", so zot and any bare caller are unchanged. (Extracted while embedding the viewer in rook - the only user-facing spot the viewer still hardcoded the "zot" name.)

## [0.9.0] - 2026-08-06

### Added

- **The read-only viewer is a public, embeddable package: `github.com/openzot/openzot/tui`.** It moved out of `internal/`, so another tool can render the same terminal view over its own agent run. The seam is the public `agent` surface - a caller supplies its own `*agent.Client` and `agent.ExecuteWithToolsOptions` (its own tools, skills and instructions), and the viewer renders the `AgentEvent` stream; the engine binding lives in the embedding application, not the view.
- **Themeable brand colour via `tui.Theme`.** An embedder sets the view's accent and secondary; `tui.DefaultTheme()` is a neutral, near-monochrome identity so a host can own the colour. The semantic status colours (running / done / failed) and the diff gutters stay fixed, so their meaning reads the same in every application.
- **The header stats bar is configurable, and now shows token usage and limit progress.** `ui.stats` (or `tui.Meta.Stats`) chooses which fields show and in what order - any of `model`, `backend`, `dir`, `iter`, `tools`, `edits`, `elapsed`, `tokens` (default: all). The `tokens` field reports the **provider's own** cumulative token counts (`↑` prompt, `↓` completion) - the real billed usage, server-side prompt caching and all, never a local estimate - surfaced live on a new `agent.UsageEvent` and accumulated in the run's `Budget`. And `iter` / `tools` / `elapsed` show progress against a configured limit (`5/1000`, `00:12/30:00`) when one is set - the 1,000,000 iteration backstop is treated as "no limit" and shows none.

### Changed

- **zot's own viewer is now neutral** (a slate accent) rather than the previous purple/pink, so it stays out of the way and reads as the default rather than a brand.
- **The header meta bar colours each field by kind** - model and backend highlighted, tool and edit counts by meaning, paths and timing muted - with fixed functional hues (independent of the brand accent), so the header carries colour and stays scannable even under a neutral theme.

### Fixed

- **The viewer's scrollback is now bounded, and configurable.** It previously kept every log line for the whole run, so a long autonomous run (unbounded tool calls, up to a million iterations) grew the viewer's memory without limit and made each redraw re-process the entire history. The on-screen log is now capped at the most recent 5,000 lines by default - the full, untrimmed run is always in the session log on disk - and a marker points there once the log has been trimmed. Keep more (or less) on screen with `ui.scrollback` (or `ZOT_UI_SCROLLBACK`); embedders set `tui.Meta.MaxScrollback`.

## [0.8.1] - 2026-08-06

### Changed

- **The default instructions now frame a run as a non-interactive batch session and tell the agent to act, not narrate**, imitating the batch-mode prompt of the engine zot was derived from: the deliverable is the changed working tree, not prose, so the model is told there is no reader to summarise or analyse tool output for, to keep working, and to end only by recording an outcome with a terminal tool.

## [0.8.0] - 2026-08-05

### Features

- **Vercel AI Gateway** is a built-in backend. `export AI_GATEWAY_API_KEY=... ; zot --backend vercel --model openai/gpt-4o "..."` - a fixed OpenAI-compatible endpoint fronting many providers, so it needs nothing but the key.
- **Cloudflare AI Gateway** is supported. Its endpoint carries your account and gateway ids, so there is no fixed URL to ship: configure it as a backend with `provider: cloudflare` and your gateway's compat URL. A run that omits the URL fails with an actionable error naming the shape to use, rather than a bare "unknown provider".
- **Bare model names are auto-qualified on gateways.** A gateway routes by a creator-qualified name (`z-ai/glm-5.2`), and each gateway spells the creator differently - OpenRouter wants `z-ai`, Vercel wants `zai`. You can now give the plain model (`--model glm-5.2`) and zot supplies the right prefix from its catalogue per gateway, because a model is the same model whichever gateway serves it. A name you qualify yourself is always sent as-is, and a model zot has not catalogued passes through bare for the gateway to resolve - zot never invents a prefix it cannot justify.
- **`plan` and `progress` tools.** The agent lays out an ordered plan with `plan` and narrates where it is with `progress` (done, current, blockers, next), and both are rendered in the viewer - so a run you are watching shows the plan it is following and how far along it is, not just a stream of tool calls. They are part of `agent.DefaultTools()`, so an embedder gets them too.
- **The task lives in the system prompt.** A run's objective is now held in the instructions rather than as the first user message, so it survives compaction - a long run cannot lose sight of what it was asked to do. `--task` / `--task-file` set that durable objective; a bare positional is promoted to it. Free-text passed alongside a task becomes an ordinary opening user message (a nudge, not the objective), and `--resume` inherits the task from the session and treats new text as the nudge.
- **Approaching-limit notices.** As a run nears a bounded limit - iterations, tool calls, or wall-clock time - the model is told, once at each of `limit_checkpoints` (default `[50, 80, 90]` percent), so it can pace itself and finish before the hard stop rather than being cut off mid-task. Each notice names which limit is approaching, in the units the model counts in, and its urgency scales with proximity: a heads-up to stay aware at the halfway mark, a nudge to prioritise as it nears, and finish-now only near the end - telling the model to wrap up at 50% would make it quit with half its budget unused. Set `limit_checkpoints: []` to turn them off.
- **The run's budgets are configurable, with matching `ZOT_AGENT_MAX_*` env overrides.** `max_settles` (how hard zot pushes the model to record an outcome), `max_calls`, `max_time`, `max_tokens`, and the `max_continuations` / `max_cycles` / `max_empties` safety guards can all be set in the config file or the environment, rather than being hard-coded. `max_time` is a wall-clock deadline checked at each step (`"30m"`, `"2h"`); `max_tokens` caps a single response.
- **Portable builds bake the configuration into the binary.** `go build -tags portable` embeds `internal/config/portable.yaml` (model, backend, even provider keys) into the executable, producing a self-contained artifact that runs with no config file and nothing to set at the destination. The baked layer is applied _last_ - above the config file and the environment - so what you compiled in is authoritative and the runtime environment cannot redirect it; fields you leave out still fall through, so you can pin the model and backend while taking the key from a `$VAR` reference. `zot --version` reports `portable config`, and the build fails loudly if the file is absent rather than shipping an unconfigured binary. The trade-off - a baked key is extractable, so the artifact becomes the secret - and the full recipe are in [docs/portable-config.md](docs/portable-config.md).
- **Configurable context-overflow strategy: `compact` (default) or `truncate`.** As the conversation approaches the model's window, `compact` now condenses the older history into a **checkpoint** with a real model call - so a long run keeps a condensed memory of its early turns rather than having them silently dropped - while `truncate` keeps the previous behaviour of trimming the oldest messages to fit. A checkpoint is pinned ahead of the conversation and never summarised again, so repeated compactions accumulate a chain of segment summaries rather than re-condensing earlier summaries into a lossy summary-of-a-summary (the mechanism the derived-from engine calls a checkpoint). Compaction is proactive and driven by the provider's reported input usage (the local estimate stands in only until the first turn reports): it fires once that usage crosses `compact_trigger_ratio` of the window and clears the `compact_min_tokens` / `compact_min_messages` floors. An outright provider rejection still falls back to the existing no-model structural summary (the request that was just rejected cannot be re-sent to a summariser), and a summariser outage degrades to that structural summary rather than stalling the run. Set via `agent.context_strategy` (or `ZOT_AGENT_CONTEXT_STRATEGY`); the trigger and floors are configurable. This ports the `thresholdStrategy` mechanism from the engine zot was derived from, which had not been carried over - only truncation had.

### Changed

- A prefixed model on a gateway resolves its real context window behind the prefix, so budgeting is sized to the actual model rather than the conservative default. (OpenRouter, already a backend, gains this along with the two new gateways.)
- **Tool calls, time, and output tokens are now unbounded by default; `max_iterations` is the only finite backstop.** A run should stop because it finished or went wrong, not because it hit an arbitrary tool-call ceiling mid-task - so `max_calls`, `max_time` and `max_tokens` impose no limit unless you set one, and 0.7.0's default tool-call budget of 1,000 is gone. The cycle, empty and continuation guards (which catch a run that has actually broken) still apply. Set any of the three explicitly to bound a run's cost or length.
- **The "factory model" table no longer claims a Verify stage the engine enforces.** Settle mode checks that the agent _declared_ an outcome, not that the work _is_ done - the agent verifies itself by running the tests because its instructions tell it to, and zot does not yet independently gate the outcome on a check of its own. The README said "Inspects results and iterates when the job is not complete" as though the engine did this; it now describes the run's real shape (the engine guarantees only the brief and the recorded outcome) and marks Verify as the agent's discipline, not an engine-enforced check.
- **A 90% test-coverage gate, enforced in CI and locally.** `make cover-check` and the CI **Coverage gate** step run one script (`scripts/coverage.sh`) that fails the build if total statement coverage drops below 90%, printing per-package numbers so a failure points at what shipped untested. A short `AGENTS.md` and an `.agents/skills/testing-and-coverage` skill record the conventions - tests assert behaviour, not constants, and are bite-checked against broken code.
- **The system prompt is configured as `instructions`.** It was `backstory`, a term carried over from a hosted product; for a tool that talks straight to any OpenAI-compatible provider, `instructions` is both the word that ecosystem's own APIs use and an accurate description of what the field holds - an operating spec, not a persona. The config key is `agent.instructions`, the env override `ZOT_AGENT_INSTRUCTIONS`, and the library surface `agent.Instructions` / `zot.DefaultInstructions`.

### Fixed

- **Loop-detection counted cumulative, not consecutive, repetitions.** The cycle budget never reset once a round came back clean, so two unrelated repetitions anywhere in a long run added up and could falsely stop it. It now zeroes on a non-cyclic round - counting consecutive cycles, as the engine zot was derived from does. (Surfaced by a parity audit against that engine.)
- **The context-trim estimate priced a tool call by its text alone**, which is empty for the request half - so an argument-heavy call (writing a big file) was counted as nearly free, letting a thread the estimate thought fit get rejected by the provider. The trimmer now counts the whole activity payload (name, arguments, result), matching the estimator used everywhere else.
- **An empty turn in settle mode now points the model at the terminal tools.** A turn producing nothing is still bounded tightly by the empty budget (a stuck model must not burn the whole settle budget on silence), but its nudge now names `_success` / `_failure` rather than telling the model it may simply "say it is finished" - which would not record the outcome settle mode requires.

## [0.7.0] - 2026-08-05

### Features

- **Native backends.** zot talks straight to a model provider over the OpenAI-compatible API, with the agentic engine running inside the binary. There is no hosted engine and no gateway in between: all you need is a provider key, and nothing leaves the machine except the request to the provider you configured. Fourteen backends ship built in - `openai`, `anthropic`, `groq`, `mistral`, `deepseek`, `openrouter`, `together`, `cerebras`, `xai`, `moonshot`, `zai`, `qwen`, `perplexity` and a local `ollama` - each reading its provider's conventional environment variable, so switching is `--backend anthropic`. Anything else that speaks the same API works as a `custom` backend with a base URL and a key.
- **The engine is zot's own.** Thread assembly, compaction, four-heuristic loop detection and settle mode are now local Go rather than calls into a hosted service. That is what makes a run inspectable and reproducible offline, and zot no longer depends on any external agent SDK.
- **`agent` is a usable library.** The same engine is importable - `agent.ExecuteWithTools` plus `agent.DefaultTools()` - so an embedder gets thread management, compaction and cycle detection without reimplementing them.
- **Session logs.** Every run is recorded to `~/.local/state/zot/sessions/` as it happens - one JSON object per line, holding the brief, the model, every message and tool call, and how the run ended. An autonomous run is unattended by definition, and this is what turns "it failed overnight" into something answerable. The log is flushed line by line, so it is readable while the run is still going and a killed run still leaves everything up to the kill.
- **`zot --resume`.** The conversation is the entire state of an agent, so writing it down and reading it back is enough to continue a run: `zot --resume last "now add the tests you skipped"`, or `--resume last` on its own to carry on with the original brief. Takes a session id, a path, or `last`. A resumed run writes its own log and records which session it continued.
- **`zot sessions`** lists previous runs, newest first, with the task and how each ended. `--no-session` records nothing; `--session-dir` / `$ZOT_SESSION_DIR` puts the logs somewhere else.
- `zot config` opens the config file in your `$EDITOR`, creating it from a commented template (embedded in the binary) on first run. This is the setup path - choose a backend and model and set your provider key by editing the file. `zot config path` prints the file location.

### Fixed

- **The module path is now `github.com/openzot/openzot`**, matching the repository. It said `github.com/chatbotkit/zot`, which meant `go install github.com/openzot/openzot/cmd/zot@latest` could not work and the version ldflags stamped a symbol that did not exist under the published path.
- The Go toolchain is pinned to 1.26.5. `govulncheck` runs in CI and 1.26.0 carried 13 reachable standard-library advisories - `crypto/x509`, `crypto/tls`, `net/http`, `net/url`, `os` - so the check failed and no release could be cut.
- The container image did not build. The Dockerfile pinned Go 1.25.12 while `go.mod` requires 1.26, and the `golang` images set `GOTOOLCHAIN=local` so the toolchain cannot be fetched - `go mod download` failed before any code was compiled. The release pipeline publishes an image, so this broke releases rather than just local builds.
- A `$VAR` reference in `api_key` was never expanded, so the literal text `$MY_KEY` was sent to the provider and came back as a 401 that reads like a bad key. `api_secret` and `authorization` - the older spellings - did expand, which is why it went unnoticed: the documented spelling was the broken one. All three now resolve, and an unset variable resolves to nothing so the run fails with "no API key configured" instead.
- Token counting was wrong in two ways, both under-counting - the direction that gets a request rejected mid-run. The per-message wire envelope was charged at 4 tokens rather than the 10 OpenAI's own cookbook uses, under-counting a thread of two hundred short messages by more than two thousand tokens; and a tool call's payload was not counted at all, so a request message - which carries no text, only a name and arguments - was priced as an empty message even when writing a whole file.
- The conversation is repaired before it goes on the wire: tool calls and their results are clustered, orphaned halves of a pair are dropped, and empty and duplicate messages removed. Providers reject the whole request rather than the invalid part, so any of these ended an otherwise healthy run with an opaque 400.
- The example config advertised `gpt-5.4-mini` on the `openai` backend after the defaults moved to `glm-5.2` on `zai`, so copying it produced different behaviour from having no config file at all. It now matches, and a test keeps them in step.

### Changed

- **Breaking (config):** a backend credential is written as `api_key` and nothing else. The `api_secret` and `authorization` spellings, the per-model `authorization`, and the convention of inlining a key into the model name (`--model 'gpt-4/authorization=sk-...'`) are all gone - they existed to let configs written for the hosted backends keep working, and those backends are gone. Per-model credentials remain, spelled `api_key`. Three spellings for one field is also what let a real bug hide: `$VAR` expansion worked in two of them and not in the documented one.
- **Breaking (security):** a released binary no longer reads a `.env` from the working directory. zot runs unattended with a provider key and a shell tool, so taking credentials from whatever directory it was pointed at is a liability - running it against a repository you cloned to review was enough to load a stray committed `.env` into the process about to run commands. `make dev` (or `go build -tags dev`) still does, for local development. The switch is a build tag that defaults to off, so a build that forgets it loses a convenience rather than a boundary; `zot --version` prints which kind you have.
- **Breaking (API):** `agent.Message` carries a typed `Activity` instead of a `Meta map[string]any`. A tool call is the only thing zot ever put there, and a map made every read a type assertion that could fail silently and every key a runtime spelling test. Logs written by earlier builds still load - the older nested shape is understood on the way in.
- **Breaking (config):** `features` is gone from the example config. It configured conversation features on a hosted engine that no longer sits in the path; zot talks straight to an OpenAI-compatible provider and has never read it.
- The default model is `glm-5.2` paired with a `zai` default backend - a default pair that cannot talk to each other is worse than no default.
- The default `max_iterations` is now 1,000,000, up from 1,000, so a long autonomous run is not cut short mid-task. The iteration count is no longer the practical bound on a run - the tool-call budget (1,000) and the cycle, empty and continuation guards are what stop one that has gone wrong. Set `--max-iterations` explicitly if you want a hard ceiling on run length.
- The provider package is organised around a `Transport` interface: chat-completions and the Responses API are peers rather than one bolted onto the other, and a future native provider (Anthropic Messages, Bedrock Converse) is one registration with nothing else to change.
- The GitHub organisation profile still described a hosted architecture - "the agentic loop runs on a capable cloud harness", a default ChatBotKit backend, and a link to get a token. All of it predates native backends.
- The example config documents the `ui` section (`diff`, `plain`), which had no example despite both being real settings and flags. `.env.example` no longer advertises removed backends, and says which build actually reads it.
- CI now vets and builds the developer variant too. It is gated behind a build tag, so nothing else in the pipeline ever compiled it - a break there would have surfaced only when someone ran `make dev`.
- `make` prints the available targets instead of assuming `build`. zot has two build variants that differ in what the binary may read from disk, and a bare `make` silently picking one is how you end up debugging a `.env` that was never going to be read. `make vet` now covers both variants, `make cross` defaults to the host, and `make race` / `make cover` were added.

## [0.6.0] - 2026-08-05

### Changed

- **Breaking (config):** the `relay` backend no longer reads a `RELAY_API_KEY` environment variable. Its credential is your own provider key, set per model (`backends.relay.models.<model>.authorization`) or as a backend-level default (`backends.relay.authorization`), or inlined into `--model` as `<model>/authorization=<key>`. A relay run with no such key fails with an actionable error rather than falling back to an env var.
- The default model is now `glm-5.2` (was `kimi-k2.7-code`).
- The example config advertises open models (`glm-5.2`, `kimi-k3`, `deepseek-v4-flash`) alongside strong OpenAI/Anthropic coding models.

## [0.5.0] - 2026-08-05

### Features

- Third backend: `chatbotkit` (`https://api.chatbotkit.com`), alongside `cbk` (now `https://api.cbk.ai`) and `relay`. `cbk` and `chatbotkit` are the same platform on its two hosts, each reading its own brand-named credential.
- Relay per-model auth: the `relay` backend now authenticates each model with its own provider key, carried inside the model string as `<model>/authorization=<key>`. zot composes it automatically from a per-model `authorization`, falling back to a backend-level `authorization` (or `$RELAY_API_KEY`); a key you inline into `--model` yourself is left untouched. This is the case a single backend key cannot express - on the relay each model can be a different provider (OpenAI, Mistral, …) with a different key.
- Per-model `authorization` under `backends.<name>.models.<model>` (joins the existing `model`, `max_iterations` and `features` overrides).
- Secret scrubbing now strips provider authorizations (backend-level and per-model) from the child-process environment in addition to Bearer secrets, so shell commands the agent runs cannot read them.

### Changed

- **Breaking (config):** the default backend is now `relay` (was `cbk`). A run with no config brings its own provider key (`RELAY_API_KEY`, or per-model `authorization`) and reaches models through `relay.cbk.ai`; target ChatBotKit explicitly with `--backend cbk` / `--backend chatbotkit`.
- **Breaking (config):** the `cbk` backend now points at `https://api.cbk.ai` and reads `CBK_API_SECRET` (previously `api.chatbotkit.com` / `CHATBOTKIT_API_SECRET`). `CHATBOTKIT_API_SECRET` now configures the new `chatbotkit` backend.
- **Breaking (config):** on the `relay` backend a single `RELAY_API_KEY` is no longer sent as a Bearer credential; it is used as the default provider key composed into the model string. Provide provider keys per model where they differ.

## [0.4.1] - 2026-07-25

### Features

- Docker: official Linux amd64 and arm64 images are published to `ghcr.io/openzot/openzot` from every release tag, with provenance attestations and an SBOM. The agent works in `/workspace`, the entrypoint is `zot` itself, and the image carries `git`, `bash`, `curl` and `openssh-client` for the agent's `exec` tool. See [docs/docker.md](docs/docker.md) for credential, config and hardening recipes.

### Changed

- Build: the module now requires Go 1.25.12, matching the toolchain the container image builds with. The previous `go 1.25.0` directive had CI building against an unpatched standard library, which `govulncheck` flags.

## [0.4.0] - 2026-07-25

### Features

- ACP mode: `zot acp` serves the same agent over the [Agent Client Protocol](https://agentclientprotocol.com/) on stdin/stdout, so an editor or agent harness can drive zot conversationally. Each session works in the directory the client supplies and keeps its own history, `AGENT.md` and skills are resolved per session, and the agent's activity streams back as ACP session updates (message chunks, tool calls, and the `plan`/`progress` tools mapped onto ACP's plan).
- Buzz: `zot acp` is a drop-in agent for [Buzz](https://github.com/block/buzz)'s `buzz-acp` harness - `BUZZ_ACP_AGENT_COMMAND=zot BUZZ_ACP_AGENT_ARGS=acp`.

## [0.3.0] - 2026-06-16

### Features

- Backends: a run now targets a named **backend**. zot ships with two - `cbk` (ChatBotKit, the default) and `relay` (CBK Relay, where you bring your own OpenAI/OpenRouter key) - both speaking the same API. Choose one per run with `--backend` (or `default_backend` in config); otherwise the default is used. The active backend is shown in the header.
- Custom models per backend: under `backends.<name>.models`, a named entry can alias a real model id and override `max_iterations` / `features`. When `--model` matches a key, that entry's settings take priority.

### Changed

- **Breaking (config):** the `chatbotkit:` section is replaced by `backends:`. The credential moves from `chatbotkit.api_secret` to `backends.cbk.api_secret`; `CHATBOTKIT_API_SECRET` still configures the default `cbk` backend with no config file. The `relay` backend's credential is `RELAY_API_KEY`.

## [0.2.0] - 2026-06-16

### Features

- Project context: `AGENT.md` (from the config directory and the working directory) is appended to the agent's backstory, and skills are loaded from `.skills/` or `skills/` in either location and passed to the agent as a `skills` feature.
- Conversation features: enable `web` and `chunking` via repeated `--feature` flags or a `features:` list in the config file (with per-feature options).
- Diff view: `--diff` (or `ui.diff` / `ZOT_UI_DIFF`) renders a framed, syntax-highlighted before/after panel under each edit/write, powered by [chroma](https://github.com/alecthomas/chroma).
- Plain mode: when stdout is not a TTY (piped, CI, driven by another program) zot streams unstyled output instead of the full-screen UI; force it with `--plain` (or `ui.plain` / `ZOT_UI_PLAIN`).

### Changed

- Default model is now `kimi-k2.7-code`, and the default iteration cap is `1000`.
- zot now builds against the published `github.com/chatbotkit/go-sdk` release; local development against an SDK checkout uses a gitignored `go.work`.

## [0.1.0] - 2026-06-15

### Features

- Initial release of `zot`, an autonomous coding agent. Brief it once and it works the problem on its own - reading files, editing them, and running shell commands - while a read-only view streams every step. No prompting, no babysitting.
- Autonomy is driven by the ChatBotKit Go SDK's `agent.ExecuteWithTools` loop (plan → act → observe → progress → exit) with `agent.DefaultTools()` (`read`, `write`, `edit`, `exec`) as the coding toolbox.
- Read-only [Bubble Tea](https://github.com/charmbracelet/bubbletea) viewer with a scrollable activity log, per-tool styling, live narration, and a header showing model, working directory, iteration, tool, and edit counters plus elapsed time. The UI has no text input by design.
- Layered configuration: built-in defaults < `~/.config/zot/config.yaml` < `ZOT_*` environment variables, with the API secret read from the platform-standard `CHATBOTKIT_API_SECRET`. CLI flags (`--model`, `--dir`, `--max-iterations`, `--task-file`, `--config`) override the resolved config.
