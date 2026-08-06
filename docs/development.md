# Building & developing zot

Building from source, the two build variants and why they differ, and a map of
the codebase. For running zot, see the [README](../README.md).

## Build from source

Requires Go 1.26+.

```bash
git clone https://github.com/openzot/openzot
cd openzot
make                     # lists the targets
make build               # or: go build -o zot ./cmd/zot
./zot --version
```

`make` on its own prints the targets rather than assuming one, because `build`
and `dev` produce binaries that differ in what they will read from disk. `make
build` stamps the version in; `make test`, `make race`, `make cover`, `make vet`
and `make cross GOOS=… GOARCH=…` are also available.

To bake a configuration - model, backend, even the provider key - _into_ the
binary so it needs nothing at the destination, build with `-tags portable`. The
compiled-in config overrides the runtime file and environment, which is the
point: nothing at the destination can redirect it. See
[portable-config.md](portable-config.md) for the recipe and the trade-offs
(chiefly: a baked key is extractable, so the artifact becomes the secret).

## Release vs developer builds

`make build` produces a release binary. `make dev` produces a developer one, and
the difference is a security boundary rather than a convenience:

|                           | release (`make build`) | developer (`make dev`) |
| ------------------------- | ---------------------- | ---------------------- |
| reads `.env` from `--dir` | no                     | yes                    |

zot runs unattended with a provider key and a shell tool, so a released binary
must not take credentials from whatever directory it was pointed at - running it
against a repository you cloned to review would otherwise be enough to load a
stray committed `.env` into the process that is about to run commands. Released
builds read credentials from the config file and the real environment, both of
which you chose deliberately.

The switch is a build tag (`-tags dev`) and defaults to off, so a build that
forgets it loses a convenience rather than a boundary. `zot --version` prints
which kind you have.

## Architecture

| Path                   | Responsibility                                                                 |
| ---------------------- | ------------------------------------------------------------------------------ |
| `cmd/zot/`             | the binary: flag parsing, sessions, working dir, then `zot.Run`                |
| `zot.go`               | embeddable core: builds the provider client + agent options and runs it        |
| `agent/`               | the public harness: `ExecuteWithTools`, tools, skills, events                  |
| `internal/loop/`       | the agentic loop: budgets, guards, settle mode, message hygiene                |
| `internal/thread/`     | context-window assembly and the four loop-detection heuristics                 |
| `internal/compaction/` | condensing older history into a summary when the window fills                  |
| `internal/provider/`   | the `Transport` seam: chat-completions and the Responses API                   |
| `internal/catalogue/`  | what each model's context window and capabilities are                          |
| `internal/tokenizer/`  | BPE token counting with embedded vocabularies                                  |
| `internal/session/`    | JSONL run logs: write, read, list, resume                                      |
| `internal/config/`     | layered config (defaults < file < env < compiled-in), XDG paths, env overrides |
| `internal/buildinfo/`  | release vs developer build, and what that changes                              |
| `internal/version/`    | build-time version stamping and GitHub update checks                           |
| `internal/tui/`        | the Bubble Tea read-only viewer (model, render, styles, agent bridge)          |
| `configs/`             | example configuration                                                          |

Contributor conventions live in [AGENTS.md](../AGENTS.md) and
[.agents/skills/](../.agents/skills/). Releasing is driven by the `VERSION` file
and the GitHub workflows - see [RELEASES.md](../RELEASES.md) and
[CHANGELOG.md](../CHANGELOG.md).
