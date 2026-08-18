# Configuring zotui

zotui is the browser command center for reusable zot workers. This guide covers
the configuration graph, local Docker development, Vercel Sandbox compute,
model credentials, storage, and the setup errors that are easiest to make.

For zot's standalone model/provider configuration, see
[providers.md](providers.md). The file described here is a separate **zotui**
configuration.

## The configuration graph

A worker created in the browser combines four configured or user-supplied
pieces:

```text
repo + environment + provider/model + mission = worker
                   |
                   +-- environment = compute + image + env vars + default provider/model
```

- A **repo** says where source code comes from and how it is authorized.
- **Compute** says where the disposable computer comes from.
- A model **provider** holds one inference connection, its credentials, and an
  optional custom model list.
- A **model** is a visible choice supplied by that custom list or ZotUI's
  built-in catalogue.
- An **environment** binds compute to a worker image, environment variables,
  and a default provider/model pair.
- The **store** keeps workers, runs, status, and terminal output after the
  browser closes.

Workers and schedules are created in the browser; they do not belong in the
YAML file.

## Create and select the config file

The default path is `$XDG_CONFIG_HOME/zotui/config.yaml`, or
`~/.config/zotui/config.yaml` when `XDG_CONFIG_HOME` is unset.

```bash
zotui config       # create the starter file and open it in $EDITOR
zotui config path  # print the selected path
```

Set `ZOTUI_CONFIG` to use another file. For repository-local development, the
ignored `.local/` directory is a useful place for a private configuration:

```bash
mkdir -p .local
cp configs/zotui.example.yaml .local/zotui.yaml
export ZOTUI_CONFIG="$PWD/.local/zotui.yaml"
```

The loader is strict: unknown or removed field names fail immediately. Values
such as `$VERCEL_TOKEN` are expanded from the zotui process environment. zotui
does not need secrets written literally into YAML, and secrets should not be
committed.

After exporting the variables referenced by the file:

```bash
zotui
# development checkout:
make dev-ui
```

The command center is available at `http://localhost:8080/workers` by default.
Set `ZOTUI_ADDR` to change the listen address.

### Web authentication

zotui does not authenticate requests to its web interface or API. Built-in web
authentication is intentionally out of scope: deployments that expose zotui
beyond the local machine are expected to place it behind a separate
authentication proxy. Configure that proxy to protect every zotui route,
including `/api/*`, and do not expose an unauthenticated zotui listener to an
untrusted network. The default loopback listen address keeps the application
local unless `ZOTUI_ADDR` is changed.

## Repositories

Repository connections are named entries under `repos`. A worker records both
the connection name and an `owner/name` repository from that connection.

### Local checkout

Use a local connection when zotui and Docker can see an existing checkout:

```yaml
repos:
  development:
    type: local
    path: $ZOTUI_REPO_PATH
    repositories:
      - openzot/openzot
```

For each run, Zotui creates a Git bundle containing the checkout's committed
refs, copies the bundle into Docker, and clones it at `/workspace`. The host
checkout is never mounted, and uncommitted or ignored host files are not copied.
A local repo therefore works only with `type: docker` compute; Zotui rejects a
remote Vercel pairing when a worker is created.

### Public GitHub repository

An explicit list of public GitHub repositories needs no credential:

```yaml
repos:
  public:
    type: github
    repositories:
      - openzot/openzot
```

Remote compute clones these repositories anonymously. The explicit list is
required; a credential-free connection cannot discover every repository on
GitHub.

### Private GitHub repositories

Use a GitHub App installation for private repositories:

```yaml
repos:
  private-github:
    type: github
    app_id: 123456
    installation_id: 7654321
    private_key: $GITHUB_APP_PRIVATE_KEY
```

Those three values are all ZotUI needs. Create the App and choose its repository
permissions in GitHub, install it on the account or organization, then copy the
App ID and installation ID and export the generated PEM private key. ZotUI uses
an App JWT to discover the installation's repositories for the worker form. It
mints a new installation token restricted to the selected repository for every
run; only that short-lived token enters the sandbox as `GH_TOKEN` and as the
credential for the initial remote clone. The App private key never leaves the
ZotUI host.

The GitHub App's own permission settings remain the upper bound. Grant only the
permissions the worker needs—for example, Contents read-only for clone-only
work, or Contents read/write and Pull requests read/write when it must push and
open pull requests. ZotUI deliberately has no permission fields that can widen
those settings.

To narrow what the command center offers without changing the installation,
add an optional lockdown list:

```yaml
    repositories:
      - acme/private-api
```

GitLab remains a configuration seam and is not implemented yet.

## Compute

Compute is independent from the model provider. Docker or Vercel runs the
computer; Z.ai, OpenAI, Anthropic, Vercel AI Gateway, or another compatible
provider runs inference.

### Local Docker

Docker compute needs no provider credential:

```yaml
compute:
  development:
    type: docker
```

When `image` is omitted, Docker uses Zotui's standard, version-pinned Microsoft
Dev Containers base. It is intentionally small and includes a shell, Git, and
curl. Zotui supplies Zot separately. Set `image` only when the project needs a
different toolchain; for example:

```yaml
environments:
  go-development:
    compute: development
    provider: vercel
    model: gateway
    image: golang:1.26.5-bookworm
```

`make dev-ui` builds and embeds release-mode Linux worker binaries automatically.
Each run gets a fresh container, receives the matching executable and private
configuration, seeds its own isolated `/workspace`, and is removed afterward,
including after cancellation. A remote repository is shallow-cloned inside the
container. A local repository connection is copied as a Git bundle and cloned;
Docker never receives a host bind mount.

### Vercel Sandbox

Vercel compute uses an access token plus the team and project IDs:

```yaml
compute:
  vercel:
    type: vercel
    token: $VERCEL_TOKEN
    team_id: $VERCEL_TEAM_ID
    project_id: $VERCEL_PROJECT_ID
    timeout: 45m
```

In the Vercel dashboard:

1. Create or select the project that will own the sandboxes and images.
2. Copy the team ID from Team Settings.
3. Copy the project ID from Project Settings.
4. Create an access token in Account Settings and scope it to that team.

`timeout` is a Go duration such as `15m`, `45m`, or `2h`. It defaults to `45m`,
the Hobby-plan maximum; use a longer value only when the Vercel plan permits it.
Vercel documents access-token and OIDC authentication in its
[Sandbox guide](https://vercel.com/docs/sandbox).

#### Standard runtime and optional custom image

When `image` is omitted, Zotui omits the image selector entirely and Vercel uses
its managed default image. Zotui does not send the legacy `runtime` property to
the `/v4/sandboxes` endpoint. The managed environment provides a full shell,
Git, and curl without requiring this project to publish a container image;
Zotui then uploads its embedded worker.

An explicit `image` is an override. It may name a Vercel-managed image or a
custom image from the project-scoped Vercel Container Registry (VCR); an
arbitrary Docker Hub, GHCR, or MCR reference cannot be shared with Docker
compute. A custom image must contain:

- Git and a shell;
- the language toolchains and utilities the worker is expected to use.

It does not contain Zot. The command center uploads its embedded Linux amd64
worker after Vercel creates the sandbox, so updating Zotui updates the worker
without rebuilding every environment image.

Creating the Vercel project and access token can be done in the web UI. Building
and pushing a project-specific toolchain image is a registry operation and uses
Docker or another OCI client. Given a local `acme-go-environment:latest` image:

```bash
export VERCEL_TOKEN="..."
export VERCEL_TEAM_ID="team_..."

printf '%s' "$VERCEL_TOKEN" | docker login vcr.vercel.com \
  --username "$VERCEL_TEAM_ID" \
  --password-stdin

docker tag acme-go-environment:latest \
  vcr.vercel.com/<team-slug>/<project-slug>/go-environment:latest
docker push vcr.vercel.com/<team-slug>/<project-slug>/go-environment:latest
```

Use slugs—not `team_...` and `prj_...` IDs—in the registry path. The repository
is created by the first push if it does not already exist. Vercel's
[VCR guide](https://vercel.com/kb/guide/how-to-use-vercel-container-registry)
covers OIDC login, access-token login, image paths, and current registry limits.

An environment in the same Vercel project can use the short image name:

```yaml
image: go-environment:latest
```

The Vercel driver creates a non-persistent sandbox, seeds the selected remote
Git repository, installs Zot and its private configuration, runs from that
repository directory, streams raw ANSI output to the browser, and stops the
sandbox at the end. Local repository connections are Docker-only.

## Providers and models

Providers and models are named separately from compute so one environment can
use different inference services without changing where code executes. They are
also independent from standalone Zot's configuration: ZotUI reads only its own
config file and writes a private per-run Zot config into each sandbox.

At least one provider must be configured, and every ZotUI provider must resolve
to at least one visible model because the worker form needs a finite list. For a
catalogued provider, omit `models` to use its built-ins. Add `models` to replace
those built-ins with a custom list.

### Direct provider

```yaml
providers:
  zai:
    api_key: $ZAI_API_KEY
    # No models block: show the built-in Z.AI catalogue.

  anthropic:
    api_key: $ANTHROPIC_API_KEY
    models:
      sonnet:
        model: claude-5-sonnet
```

The provider map key is the Zot driver by default. Set `driver` when the
connection name is an alias, or when a custom endpoint uses another transport:

```yaml
providers:
  corporate:
    driver: openai
    base_url: https://models.example.com/v1
    api_key: $CORPORATE_MODEL_KEY
    models:
      fast:
        model: gpt-5.4-mini
```

Provider keys and endpoints are held by ZotUI and written into the sandbox's
private Zot configuration for that run. They are not baked into the worker
image. The browser receives only provider names and model aliases. In the
worker form, selecting a provider shows that provider's built-in or custom
models. Custom map keys are the names shown in the UI; an omitted `model` uses
the map key as the provider model ID.

### Vercel AI Gateway

Vercel AI Gateway is a model provider, not Vercel Sandbox authentication:

```yaml
providers:
  vercel:
    api_key: $AI_GATEWAY_API_KEY
    models:
      gateway:
        model: openai/gpt-5.4
```

Create this key from **Vercel Dashboard → AI Gateway → API Keys → Create Key**.
The gateway exposes an OpenAI-compatible endpoint and uses creator-qualified
model IDs such as `openai/gpt-5.4` or `anthropic/claude-sonnet-4.6`. See Vercel's
[AI Gateway authentication guide](https://vercel.com/docs/ai-gateway/authentication-and-byok)
and zot's [provider guide](providers.md#gateways-and-prefixed-models).

These keys are not interchangeable:

| Variable | Purpose |
| --- | --- |
| `VERCEL_TOKEN` | Create and manage Vercel sandboxes through the Vercel account API |
| `AI_GATEWAY_API_KEY` | Send model requests through Vercel AI Gateway |

You can use Vercel Sandbox with a direct Z.ai key, or Docker compute with
Vercel AI Gateway. Neither product requires the other.

## Environments

An environment is the reusable runtime blueprint selected in the worker form:

```yaml
environments:
  go-development:
    compute: development
    provider: vercel
    model: gateway
    image: golang:1.26.5-bookworm
    env:
      GOFLAGS: -mod=mod
    repositories:
      - development/openzot/openzot
```

- `compute` references a key under `compute`.
- `provider` references a key under `providers`.
- `model` is the default built-in or custom model for that provider; the worker
  may select another provider/model pair.
- `image` is optional. Omit it for Zotui's standard environment; set it to an
  image understood by the selected compute provider when a custom toolchain is
  needed (a Docker registry reference for Docker, or a Vercel-managed/VCR image
  for Vercel).
- `env` becomes the sandbox's baseline environment.
- `repositories` is an optional allowlist. Each entry is
  `repo-connection/owner/name`, not just `owner/name`.

The repository connection and environment allowlists are both restrictions.
Neither can widen access granted by the other.

## Store

SQLite is the only implemented store today:

```yaml
store:
  driver: sqlite
  dsn: $ZOTUI_STORE_DSN
```

For repository-local development:

```bash
export ZOTUI_STORE_DSN="$PWD/.local/state/zotui.db"
```

The database contains worker definitions, schedules, run records, status, and
terminal output. Keep it on persistent storage if run history matters. Closing
the browser or restarting zotui does not remove it.

## Complete local Docker example

```yaml
repos:
  development:
    type: local
    path: $ZOTUI_REPO_PATH
    repositories:
      - openzot/openzot

compute:
  development:
    type: docker

providers:
  vercel:
    api_key: $AI_GATEWAY_API_KEY
    models:
      gateway:
        model: openai/gpt-5.4

environments:
  go-development:
    compute: development
    provider: vercel
    model: gateway
    env:
      GOFLAGS: -mod=mod
    repositories:
      - development/openzot/openzot

store:
  driver: sqlite
  dsn: $ZOTUI_STORE_DSN
```

```bash
export ZOTUI_CONFIG="$PWD/.local/zotui.yaml"
export ZOTUI_REPO_PATH="$PWD"
export ZOTUI_STORE_DSN="$PWD/.local/state/zotui.db"
export AI_GATEWAY_API_KEY="..."
make dev-ui
```

## Complete Vercel smoke-test example

This example combines a public GitHub repo, Vercel Sandbox compute, and Vercel
AI Gateway. The two Vercel credentials remain separate.

```yaml
repos:
  public:
    type: github
    repositories:
      - openzot/openzot

compute:
  vercel:
    type: vercel
    token: $VERCEL_TOKEN
    team_id: $VERCEL_TEAM_ID
    project_id: $VERCEL_PROJECT_ID
    timeout: 45m

providers:
  vercel:
    api_key: $AI_GATEWAY_API_KEY
    models:
      gateway:
        model: openai/gpt-5.4

environments:
  vercel-go:
    compute: vercel
    provider: vercel
    model: gateway
    repositories:
      - public/openzot/openzot

store:
  driver: sqlite
  dsn: $ZOTUI_STORE_DSN
```

## Troubleshooting

### `provider: no API key configured`

The selected provider's `api_key` expanded to an empty string. Export the named
variable in the same shell that starts zotui. A Vercel Sandbox token is not a
model API key.

### `unknown model "..."`

The worker still references a model name that is absent from `models`. Edit the
worker after renaming a model, or recreate it with one of the configured model
names.

### `local repo ... requires docker compute`

A local checkout can only be bundled by Docker compute on the Zotui host. Select
a Docker environment or configure a remote GitHub repo connection.

### Vercel reports an unknown or invalid image

Remove `image` to use Vercel's managed default image. If a custom toolchain is
intentional, confirm that it was pushed to the same Vercel project named by
`project_id`, then check the project's Images view for the repository and tag.
Vercel does not accept an arbitrary external registry reference here. Use the
fully-qualified VCR path temporarily if the short name is ambiguous.

### A run stops around its configured timeout

`timeout` limits the Vercel sandbox lifetime independently from Zot's iteration
budget. Increase it only within the account plan's Sandbox limit.

### The UI still shows old workers or runs

Workers and runs live in the SQLite store, not in the YAML file. Editing YAML
changes available connections, models, and environments; it does not rewrite
existing worker records. Use a different `dsn` for a clean development store or
edit/recreate the worker.
