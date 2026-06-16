# Changelog

All notable changes to zot, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-06-15

### Features

- Initial release of `zot`, an autonomous coding agent you watch, not drive. Hand it a single task on the command line and it works the problem on its own — reading files, editing them, and running shell commands — while the terminal streams a live, read-only view of every step.
- Autonomy is driven by the ChatBotKit Go SDK's `agent.ExecuteWithTools` loop (plan → act → observe → progress → exit) with `agent.DefaultTools()` (`read`, `write`, `edit`, `exec`) as the coding toolbox.
- Read-only [Bubble Tea](https://github.com/charmbracelet/bubbletea) viewer with a scrollable activity log, per-tool styling, live narration, and a header showing model, working directory, iteration, tool, and edit counters plus elapsed time. The UI has no text input by design.
- Layered configuration: built-in defaults < `~/.config/zot/config.yaml` < `ZOT_*` environment variables, with the API secret read from the platform-standard `CHATBOTKIT_API_SECRET`. CLI flags (`--model`, `--dir`, `--max-iterations`, `--task-file`, `--config`) override the resolved config.
