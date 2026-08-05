# Changelog

All notable changes to zot, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

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
