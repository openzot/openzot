# Changelog

All notable changes to zot, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

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

- The container image did not build. The Dockerfile pinned Go 1.25.12 while `go.mod` requires 1.26, and the `golang` images set `GOTOOLCHAIN=local` so the toolchain cannot be fetched - `go mod download` failed before any code was compiled. The release pipeline publishes an image, so this broke releases rather than just local builds.
- A `$VAR` reference in `api_key` was never expanded, so the literal text `$MY_KEY` was sent to the provider and came back as a 401 that reads like a bad key. `api_secret` and `authorization` - the older spellings - did expand, which is why it went unnoticed: the documented spelling was the broken one. All three now resolve, and an unset variable resolves to nothing so the run fails with "no API key configured" instead.
- Token counting was wrong in two ways, both under-counting - the direction that gets a request rejected mid-run. The per-message wire envelope was charged at 4 tokens rather than the 10 OpenAI's own cookbook uses, under-counting a thread of two hundred short messages by more than two thousand tokens; and a tool call's payload was not counted at all, so a request message - which carries no text, only a name and arguments - was priced as an empty message even when writing a whole file.
- The conversation is repaired before it goes on the wire: tool calls and their results are clustered, orphaned halves of a pair are dropped, and empty and duplicate messages removed. Providers reject the whole request rather than the invalid part, so any of these ended an otherwise healthy run with an opaque 400.
- The example config advertised `gpt-5.4-mini` on the `openai` backend after the defaults moved to `glm-5.2` on `zai`, so copying it produced different behaviour from having no config file at all. It now matches, and a test keeps them in step.

### Changed

- **Breaking (security):** a released binary no longer reads a `.env` from the working directory. zot runs unattended with a provider key and a shell tool, so taking credentials from whatever directory it was pointed at is a liability - running it against a repository you cloned to review was enough to load a stray committed `.env` into the process about to run commands. `make dev` (or `go build -tags dev`) still does, for local development. The switch is a build tag that defaults to off, so a build that forgets it loses a convenience rather than a boundary; `zot --version` prints which kind you have.
- **Breaking (API):** `agent.Message` carries a typed `Activity` instead of a `Meta map[string]any`. A tool call is the only thing zot ever put there, and a map made every read a type assertion that could fail silently and every key a runtime spelling test. Logs written by earlier builds still load - the older nested shape is understood on the way in.
- **Breaking (config):** `features` is gone from the example config. It configured conversation features on a hosted engine that no longer sits in the path; zot talks straight to an OpenAI-compatible provider and has never read it.
- The default model is `glm-5.2` paired with a `zai` default backend - a default pair that cannot talk to each other is worse than no default.
- The default `max_iterations` is now 1,000,000, up from 1,000, so a long autonomous run is not cut short mid-task. The iteration count is no longer the practical bound on a run - the tool-call budget (1,000) and the cycle, empty and continuation guards are what stop one that has gone wrong. Set `--max-iterations` explicitly if you want a hard ceiling on run length.
- The provider package is organised around a `Transport` interface: chat-completions and the Responses API are peers rather than one bolted onto the other, and a future native provider (Anthropic Messages, Bedrock Converse) is one registration with nothing else to change.
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
