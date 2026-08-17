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

## Development container

The repository includes a standard [Dev Container](https://containers.dev/)
under [`.devcontainer/`](../.devcontainer/). Open the checkout in a compatible
editor and choose **Reopen in Container**. It provides the pinned Go toolchain,
Git plus the official Git LFS Dev Container Feature, Make, ripgrep, SQLite, and
Docker-in-Docker. Go's build and module caches, Docker data, and zotui's SQLite
state live in named volumes, so rebuilding the container does not throw them away.

Once the container is ready:

```bash
make test
make dev-ui
```

The second command serves the command center on port 8080, which the container
forwards to the host. It uses `.devcontainer/zotui.dev.yaml`: a credential-free
fixture whose local repo points at this checkout and whose `development` compute
launches an ephemeral Docker container per run. The checkout is mounted at
`/workspace`; the model credential, if supplied, is written into the container's
temporary zot config and the container is removed when the run ends.

The dev container builds the Linux amd64 and arm64 Zot worker artifacts during
setup. Zotui embeds those compressed executables and transfers the matching one
into every sandbox it creates. Environment images therefore contain only their
toolchain and dependencies; they do not need to install or track Zot.

`make dev-ui` generates the worker artifacts automatically and also works
outside the container when a Docker daemon is available. The local SQLite
database is kept under the ignored `.local/` directory unless
`ZOTUI_STORE_DSN` overrides it.
See [zotui-configuration.md](zotui-configuration.md) for complete local Docker
and Vercel Sandbox configurations, model credentials, and repository setup.

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
| `cmd/zotui/`           | browser command center entrypoint and HTTP server lifecycle                    |
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
| `internal/zotui/`      | workers, runs, schedules, dispatch, persistence, and embedded browser app      |
| `tui/`                 | public Bubble Tea read-only viewer (themeable; embeddable over any `agent` run) |
| `configs/`             | example configuration                                                          |

Contributor conventions live in [AGENTS.md](../AGENTS.md) and
[.agents/skills/](../.agents/skills/). Releasing is driven by the `VERSION` file
and the GitHub workflows - see [RELEASES.md](../RELEASES.md) and
[CHANGELOG.md](../CHANGELOG.md).
