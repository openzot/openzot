# Running zot in a container

zot is an autonomous agent with real file-write and shell-exec access to its
working directory. A container is the most practical way to bound that: the
agent can only change what you mounted, and the usual `docker run` levers
(dropped capabilities, a read-only root filesystem, resource limits) apply
without zot having to implement a sandbox of its own.

This document covers the published image. For the agent itself - flags, config,
backends - see the [README](../README.md).

## The image

```bash
docker pull ghcr.io/openzot/openzot:latest
```

| | |
| --- | --- |
| Registry | `ghcr.io/openzot/openzot` |
| Tags | `vX.Y.Z`, `X.Y.Z`, `X.Y`, and `latest` for stable releases. Prereleases (`v0.5.0-beta.1`) publish only their exact tags and never move `latest`. |
| Platforms | `linux/amd64`, `linux/arm64` |
| Entrypoint | `zot` - arguments after the image name are zot's own |
| Base | `alpine:3.22` plus `bash`, `ca-certificates`, `curl`, `git`, `openssh-client`, `tzdata` |

The runtime packages are there because the agent's `shell` tool runs real
commands: a task that clones, builds, or commits needs them. Anything else your
task needs (a language toolchain, a package manager) has to come from an image
built `FROM ghcr.io/openzot/openzot` - see [Extending](#extending-the-image).

### Layout

| Path | Purpose |
| --- | --- |
| `/workspace` | Working directory. Mount your checkout here. |
| `/home/zot/.config/zot/config.yaml` | Config file, pointed at by `ZOT_CONFIG`. Absent by default - zot runs on defaults plus env vars. |
| `/usr/local/share/zot/zot.example.yaml` | The documented example config, for copying out. |
| `/usr/local/bin/zot` | The binary. |

`HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and
`XDG_RUNTIME_DIR` are all set under `/home/zot`. The image user is `zot`
(uid/gid 10001).

## Running a task

zot talks straight to a model provider, so a run needs nothing but that
provider's key. These examples use OpenAI; swap the variable and `--backend` for
any other - see [Backends](../README.md#backends) for the full list.

```bash
docker run --rm -it \
  --user "$(id -u):$(id -g)" \
  --env HOME=/tmp \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest "add input validation to the signup handler and a test"
```

Two flags need explaining, and both are about **bind mounts**, not about zot:

- **`--user "$(id -u):$(id -g)"`.** A host directory keeps its host ownership
  inside the container, so the default uid 10001 cannot write to a checkout
  owned by you. Running as yourself fixes that in both directions: the agent can
  edit the files, and everything it creates belongs to you afterwards rather
  than to uid 10001.
- **`--env HOME=/tmp`.** `/home/zot` belongs to the image user, so an
  overridden uid has no writable home. Tools that keep state there - `git
  config --global`, an SSH `known_hosts` - fail without this. `/tmp` is
  world-writable and disposable, which is what you want for a one-shot run.

Neither is needed when the workspace is a **named volume**, because Docker
initialises it from the image and the default user owns it:

```bash
docker run --rm -it \
  --env OPENAI_API_KEY \
  --volume zot-workspace:/workspace \
  ghcr.io/openzot/openzot:latest "scaffold a tiny snake game in python"
```

That is the safest shape available: the run cannot see your filesystem at all.
Retrieve the result with `docker cp`, or seed the volume first from a git clone
the agent performs itself.

### Credentials

The backend credential is an environment variable - never bake it into an image
or a config file you push anywhere:

```bash
# pass through a variable already exported in your shell
docker run --env OPENAI_API_KEY …

# or from a file the daemon reads at run time
docker run --env-file ./zot.env …
```

Released binaries - the published image included - deliberately do **not** read
a `.env` from the working directory. A container run mounts a checkout it did
not write, and reading credentials out of it would mean a stray committed `.env`
in the code under review could feed the process that is about to run shell
commands. Pass credentials with `--env` or `--env-file`, where the value comes
from your secret store. (A developer build, `make dev`, does read `.env`; see
[Developer builds](../README.md#developer-builds).)

After zot loads its configuration, configured backend credentials are removed
from the environment inherited by agent shell commands, so the task itself
cannot read the key that is paying for it.

Use a key scoped to the narrowest thing that works - a project-scoped provider
key rather than an account-wide one. A container run unattended is exactly the
case the scoping is for.

### Config, `AGENT.md` and skills

Per-project context needs nothing: `AGENT.md` and `.skills/` are read from the
working directory, which is the volume you mounted.

Global context lives in the config directory, so mount it read-only:

```bash
docker run --rm -it \
  --user "$(id -u):$(id -g)" --env HOME=/tmp \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  --volume "$HOME/.config/zot":/home/zot/.config/zot:ro \
  ghcr.io/openzot/openzot:latest "…"
```

`ZOT_CONFIG` already points at `/home/zot/.config/zot/config.yaml`; a missing
file there is not an error. Single settings are easier as environment
variables - `ZOT_AGENT_MODEL`, `ZOT_AGENT_MAX_ITERATIONS`, `ZOT_DEFAULT_BACKEND`
- and they override the file.

### Terminal or not

With `-it` you get the full-screen viewer. Without a TTY zot detects it and
streams plain unstyled output instead, which is what you want from a pipeline:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" --env HOME=/tmp \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest --max-iterations 40 --task-file TASK.md | tee run.log
```

`--max-iterations` is worth setting explicitly for unattended runs. The default
is deliberately enormous, so it is the tool-call budget and the cycle, empty and
continuation guards - not the iteration count - that actually end a run that has
gone wrong. If you want a hard ceiling on how long a run may go, set this.


## Hardening

The image already runs as a non-root user with no setuid needs. For unattended
runs, add the rest:

```bash
docker run --rm \
  --user "$(id -u):$(id -g)" --env HOME=/tmp \
  --read-only --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 512 --memory 2g --cpus 2 \
  --env OPENAI_API_KEY \
  --volume "$PWD":/workspace \
  ghcr.io/openzot/openzot:latest --task-file TASK.md
```

`--read-only` works because everything zot writes goes to the workspace volume;
the `--tmpfs /tmp` covers `HOME` and whatever the agent's shell commands scratch
down. Note that a read-only root also stops the agent from installing packages
mid-run, which is usually what you want and occasionally the thing that breaks a
task.

**Egress cannot be closed.** The agentic loop runs in the container, but the
model does not, so it needs outbound HTTPS to whichever provider it is
configured against - `api.openai.com`, `api.anthropic.com`, and so on - plus
whatever the task itself fetches. `--network none` gives you a container that
cannot do anything. If you need containment, restrict egress to that one host
rather than removing it.

**The mount is the blast radius.** Mount the narrowest thing that lets the task
succeed - one repository, not a parent directory of every repository. Prefer
`:ro` for anything the agent only needs to read.

## Extending the image

A task that builds Go, installs npm packages, or runs a database client needs
those tools present. Layer them on:

```dockerfile
FROM ghcr.io/openzot/openzot:v0.4.1

USER root
RUN apk add --no-cache go nodejs npm make
USER zot
```

Pin the base tag rather than tracking `latest`, so an agent's behaviour does not
change under a build you did not intend.

## Building locally

```bash
docker build --build-arg VERSION=v0.4.1 --tag openzot/zot:local .
docker run --rm openzot/zot:local --version
```

`VERSION` is what gets stamped into the binary and reported by `--version`;
without it the build produces `dev` and the update check is skipped. Note that
`go.work` is excluded from the build context by `.dockerignore` - a workspace
file someone created locally must not leak into an image build and cap the
toolchain below what `go.mod` requires.

Images are always built without `-tags dev`, so a published image never reads a
`.env` from the directory you mounted. See
[Credentials](#credentials).

Published images are built by
[`release.yaml`](../.github/workflows/release.yaml) on every `v*` tag; CI builds
and smoke-tests the same Dockerfile on every code push. See
[RELEASES.md](../RELEASES.md).

## See also

- [README](../README.md) - flags, config and backends
- [Pantalk deployment](https://github.com/pantalk/pantalk/blob/main/docs/deployment.md) -
  putting a containerised agent into chat
- [MCPShim deployment](https://github.com/mcpshim/mcpshim/blob/main/docs/deployment.md) -
  giving a containerised agent tools
