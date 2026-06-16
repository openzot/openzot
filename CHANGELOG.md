# Changelog

All notable changes to zot, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [0.2.0] - 2026-06-16

### Features

- Project context: `AGENT.md` (from the config directory and the working directory) is appended to the agent's backstory, and skills are loaded from `.skills/` or `skills/` in either location and passed to the agent as a `skills` feature.
- Conversation features: enable `web` and `chunking` via repeated `--feature` flags or a `features:` list in the config file (with per-feature options).
- Diff view: `--diff` (or `ui.diff` / `ZOT_UI_DIFF`) renders a framed, syntax-highlighted before/after panel under each edit/write, powered by [chroma](https://github.com/alecthomas/chroma).
- Plain mode: when stdout is not a TTY (piped, CI, driven by another program) zot streams unstyled output instead of the full-screen UI; force it with `--plain` (or `ui.plain` / `ZOT_UI_PLAIN`).
- The API token can now be set in the config file under `chatbotkit.api_secret`, in addition to `CHATBOTKIT_API_SECRET`.

### Changed

- Default model is now `kimi-k2.7-code`, and the default iteration cap is `1000`.
- zot now builds against the published `github.com/chatbotkit/go-sdk` release; local development against an SDK checkout uses a gitignored `go.work`.

## [0.1.0] - 2026-06-15

### Features

- Initial release of `zot`, an autonomous coding agent. Brief it once and it works the problem on its own - reading files, editing them, and running shell commands - while a read-only view streams every step. No prompting, no babysitting.
- Autonomy is driven by the ChatBotKit Go SDK's `agent.ExecuteWithTools` loop (plan → act → observe → progress → exit) with `agent.DefaultTools()` (`read`, `write`, `edit`, `exec`) as the coding toolbox.
- Read-only [Bubble Tea](https://github.com/charmbracelet/bubbletea) viewer with a scrollable activity log, per-tool styling, live narration, and a header showing model, working directory, iteration, tool, and edit counters plus elapsed time. The UI has no text input by design.
- Layered configuration: built-in defaults < `~/.config/zot/config.yaml` < `ZOT_*` environment variables, with the API secret read from the platform-standard `CHATBOTKIT_API_SECRET`. CLI flags (`--model`, `--dir`, `--max-iterations`, `--task-file`, `--config`) override the resolved config.
