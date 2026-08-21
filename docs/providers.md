# Providers: connecting zot to inference

A provider is a named inference connection: a driver, endpoint, and credential.
It may reach a model service directly, a local server, or a gateway. Pick one per
run with `--provider`, or set `default_provider` in config.

For the agent itself - the run loop, tools, safety - see the [README](../README.md).

## The built-in providers

| Provider     | Endpoint                          | Credential from      |
| ------------ | --------------------------------- | -------------------- |
| `openai`     | `https://api.openai.com/v1`       | `OPENAI_API_KEY`     |
| `anthropic`  | `https://api.anthropic.com/v1`    | `ANTHROPIC_API_KEY`  |
| `groq`       | `https://api.groq.com/openai/v1`  | `GROQ_API_KEY`       |
| `mistral`    | `https://api.mistral.ai/v1`       | `MISTRAL_API_KEY`    |
| `deepseek`   | `https://api.deepseek.com/v1`     | `DEEPSEEK_API_KEY`   |
| `openrouter` | `https://openrouter.ai/api/v1`    | `OPENROUTER_API_KEY` |
| `together`   | `https://api.together.xyz/v1`     | `TOGETHER_API_KEY`   |
| `cerebras`   | `https://api.cerebras.ai/v1`      | `CEREBRAS_API_KEY`   |
| `xai`        | `https://api.x.ai/v1`             | `XAI_API_KEY`        |
| `moonshot`   | `https://api.moonshot.cn/v1`      | `MOONSHOT_API_KEY`   |
| `zai`        | `https://api.z.ai/api/paas/v4`    | `ZAI_API_KEY`        |
| `qwen`       | DashScope compatible mode         | `DASHSCOPE_API_KEY`  |
| `vercel`     | `https://ai-gateway.vercel.sh/v1` | `AI_GATEWAY_API_KEY` |
| `ollama`     | `http://localhost:11434/v1`       | none                 |

## Credentials

Each built-in provider reads its conventional credential variable, so switching
provider is a pair of flags:

```bash
export ANTHROPIC_API_KEY="sk-ant-…"
zot --provider anthropic --model claude-5-sonnet "…"
```

Give the model as well as the provider. The default model is `glm-5.2` and it
only means something on `zai`; a provider and a model that cannot talk to each
other fail as a provider error rather than a configuration one, which is much
harder to read.

A local model needs no key at all:

```bash
zot --provider ollama --model llama-4 "…"
```

To make a different pair the default, set both in config:

```yaml
# ~/.config/zot/config.yaml
default_provider: openai
agent:
  model: gpt-5.4-mini
```

Any key can equally live in the config file (`~/.config/zot/config.yaml`, or the
path given to `--config`), including as a `$ENV_VAR` reference so no secret is
written to disk:

```yaml
providers:
  openai:
    api_key: '$OPENAI_API_KEY'
```

The provider name normally selects its driver, so `openai:` means
`driver: openai`. Set the driver explicitly when the connection has a local
alias:

```yaml
default_provider: corporate
providers:
  corporate:
    driver: openai
    base_url: https://models.example.com/v1
    api_key: '$CORPORATE_MODEL_KEY'
```

The driver selects Zot's endpoint defaults and provider-specific behavior; the
map key remains the name used by `--provider` and `default_provider`.

## Built-in and custom model lists

The `models` block on a provider is optional. Without it, Zot accepts the model
named by `agent.model` or `--model`; catalogued models contribute their known
context and capabilities, while a newly released unknown model still runs with
conservative defaults.

Define `models` when a connection should expose a deliberate custom list. Its
map keys become the allowed names for that provider, and each entry can alias
the real model ID or override model-specific settings:

```yaml
default_provider: corporate
agent:
  model: fast

providers:
  corporate:
    driver: openai
    base_url: https://models.example.com/v1
    api_key: $CORPORATE_MODEL_KEY
    models:
      fast:
        model: gpt-5.4-mini
        max_iterations: 50
      deep:
        model: gpt-5.4
```

When a custom list exists, selecting any other model is a configuration error.
This makes the list useful as an intentional connection boundary. Omit it when
you want Zot's permissive built-in/unknown-model behavior.

## Gateways and prefixed models

`openrouter` and `vercel` are model gateways: one endpoint fronting many
providers, addressed by a provider-qualified model name like `openai/gpt-5.4` or
`anthropic/claude-5-sonnet`. zot resolves the model's real context window behind
the prefix, and - because a model is the same model whichever gateway serves it -
you can give a **bare** name and zot supplies each gateway's own prefix from its
catalogue:

```bash
export OPENROUTER_API_KEY="sk-..."
zot --provider openrouter --model glm-5.2 "…"   # sent as z-ai/glm-5.2

export AI_GATEWAY_API_KEY="..."                # Vercel AI Gateway
zot --provider vercel --model glm-5.2 "…"       # sent as zai/glm-5.2
```

A name you qualify yourself (`--model z-ai/glm-5.2`) is always sent as-is, and a
model zot has not catalogued passes through bare for the gateway to resolve.

**Cloudflare AI Gateway** is supported too, but its endpoint carries your account
and gateway ids, so there is no fixed URL to ship - configure the `cloudflare`
provider with your gateway's compat URL:

```yaml
default_provider: cloudflare
providers:
  cloudflare:
    base_url: https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/compat
    api_key: '$OPENAI_API_KEY' # the downstream provider's key
```

## Any other provider

Anything that speaks the OpenAI chat-completions API works. Name a provider, give
it a base URL and a key:

```yaml
default_provider: mygateway

providers:
  mygateway:
    driver: custom
    base_url: https://gateway.internal.example.com/v1
    api_key: '$GATEWAY_KEY'
```

The endpoint must be `https` unless it is loopback, and a custom endpoint needs
its own key - a credential is scoped to the host it was issued for, and zot will
not forward one to a URL you just typed.

## The Responses API

On OpenAI, reasoning models use the
[Responses API](https://platform.openai.com/docs/api-reference/responses)
automatically. It carries reasoning state between tool rounds as an opaque item
the model resumes from; chat-completions has nowhere to put it, so a reasoning
model driven that way re-derives its thinking on every round.

Only OpenAI implements it today, so everywhere else stays on chat-completions.
