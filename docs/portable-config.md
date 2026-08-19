# Portable builds: compiling the configuration into the binary

By default zot is configured at runtime - a config file under `~/.config/zot/`,
or environment variables, resolved when it starts. A **portable build** instead
bakes a configuration *into the binary*: the model, the default provider, and
even the provider keys travel inside the executable. The result is a single
self-contained artifact that runs with no config file and nothing to set at the
destination.

For the runtime configuration model - the file, the environment, the flags - see
[configuration.md](configuration.md). This document is only about the
compiled-in layer.

## When you would want this

- **A drop-in binary for a fixed provider.** Hand someone a `zot` that already
  knows which model and provider to use, with the key inside it - no setup, no
  "export this variable first", no config file to copy and keep in sync.
- **Locked-down or ephemeral environments.** CI runners, kiosks, appliances,
  containers built from scratch - anywhere writing a config file or exporting a
  secret is awkward or impossible, the binary carries everything it needs.
- **Protecting the configuration from the runtime.** The baked values override
  the config file and the environment (see [Layering](#layering)), so a stray
  `~/.config/zot/config.yaml` or a `ZOT_AGENT_MODEL` in the environment cannot
  silently redirect a portable binary to a different model or provider. What you
  compiled in is what runs.

## The recipe

The configuration is embedded from `internal/config/portable.yaml`, a file that
is **not** committed (it usually holds credentials). Create it from the tracked
template, fill it in, and build with the `portable` tag:

```bash
# from the tool directory
cp internal/config/portable.example.yaml internal/config/portable.yaml
$EDITOR internal/config/portable.yaml          # set model, provider, keys

go build -tags portable -o zot ./cmd/zot
```

`internal/config/portable.yaml` uses the **same schema** as
[`configs/zot.example.yaml`](../configs/zot.example.yaml) - it is just a config
file that ends up inside the binary instead of on disk. A minimal one:

```yaml
agent:
  model: 'glm-5.2'
default_provider: zai
providers:
  zai:
    api_key: 'sk-...'      # baked in verbatim
```

If `portable.yaml` is missing when you build with `-tags portable`, the build
fails with `pattern portable.yaml: no matching files found`. That is deliberate:
a portable binary with no baked config would be a silent mistake, so the embed
refuses rather than producing one.

Confirm what you built:

```bash
$ zot --version
zot v0.8.0 (release, portable config)
```

The `portable config` marker is there so "why is it ignoring my config file" is
answerable without reading the source.

## Layering

A portable build does not replace runtime configuration - it sits **on top** of
it. Resolution order, lowest priority first:

```
built-in defaults  <  config file  <  environment  <  compiled-in (portable)
```

Two consequences follow, and both are the point:

- **A field you bake in is authoritative.** It overrides the config file and the
  environment, so the deployment cannot change it. Bake `model` and
  `default_provider` and the binary runs that pair, whatever the destination's
  config file or `ZOT_*` variables say.
- **A field you leave out falls through.** The overlay only sets the fields
  present in `portable.yaml`; everything else still resolves from the file, the
  environment, and the defaults. So you can bake the model and provider while
  leaving the *key* to the environment - see below.

One thing sits above even the portable layer: an **explicit command-line flag**.
`--model` / `--provider` are an operator running the binary deliberately choosing
otherwise, and they still win. Portable protects against the ambient
environment, not against the person at the keyboard. If you need to forbid that
too, do not expose those flags to whoever runs it (for example, wrap the binary
or fix them in a container entrypoint).

### Baking a key vs. baking a reference

How you write the credential decides whether the binary is self-contained:

```yaml
providers:
  zai:
    api_key: 'sk-...'              # the literal key is compiled in - fully self-contained
  openai:
    api_key: '$OPENAI_API_KEY'     # the *reference* is compiled in - still read at runtime
```

A literal key makes the binary run anywhere with no environment at all. A
`$VAR` reference is baked in **as the reference**, not as its current value, so
the binary still reads that variable when it starts - use it to pin the model
and provider while keeping the secret out of the artifact.

## The trade-offs

A baked key buys convenience with real downsides. Know them before you ship one:

- **A compiled-in key is extractable.** `strings zot | grep sk-` will find it.
  Embedding a secret in a binary is obfuscation, not encryption: **treat the
  artifact itself as the secret.** Distribute a key-baked binary only as widely
  as you would distribute the key, and never publish one.
- **Rotating a key means rebuilding.** The credential is frozen at build time.
  When it changes, you rebuild and redistribute - there is no config file to
  edit at the destination. For keys that rotate, prefer baking a `$VAR`
  reference and keeping the value in the environment.
- **The configuration is version-pinned.** The config and the code ship as one
  unit, so a config change and a binary upgrade are the same event. That is an
  upside for reproducibility and a downside for flexibility - decide which you
  want per deployment.
- **It is easy to forget which binary is which.** Two builds of the same version
  can carry different baked configs. `zot --version` flags a portable build, but
  it does not say *what* was baked - track that yourself.

## A note on the agent's shell

zot already scrubs resolved provider credentials from the process environment
before the agent's `shell` tool can run, so the commands it executes do not
inherit your API keys (see [Safety](../README.md#-safety)). A **baked** key is
handled the same way - and it was never in the environment to begin with, so a
portable binary with a compiled-in key gives the agent's shell *nothing* to read
from the environment for that credential. That is a genuine, if narrow, security
improvement over `export OPENAI_API_KEY=...`.

The offsetting risk is the artifact: the key is now inside a file that is easy
to copy. The two considerations are independent - a baked key is harder for the
running agent to exfiltrate from its environment, and easier for anyone holding
the binary to extract at rest. Weigh both for your threat model.
