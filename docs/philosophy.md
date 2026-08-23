# Why zot exists

zot is not another coding agent you sit with and steer prompt by prompt. Its
reason to exist is **fully autonomous software work**: give it a goal, walk away,
and come back to a finished result. It will sometimes fall short of that ideal,
especially while it is 0.x, but the ideal is the design constraint. We are not
building workflows or features that contradict it.

If what you want is an interactive coding agent, use
[Codex](https://openai.com/codex/),
[Claude Code](https://www.anthropic.com/claude-code),
[Cursor](https://www.cursor.com/), or one of the other excellent tools built for
that way of working.

## The production run, not the conversation

Most coding tools optimize the conversation between a developer and an agent.
zot optimizes the production run: it drives the whole job from one brief, without
a follow-up prompt at every step and without a hosted engine.

It talks straight to a model provider over the OpenAI-compatible API - OpenAI,
Anthropic, Groq, Mistral, DeepSeek, OpenRouter, a local Ollama, or anything else
that speaks the same API - so all you need is a provider key. No account beyond
the provider's own, and nothing is sent anywhere except the provider you
configure. See [providers.md](providers.md).

## Work orders, not prompts

zot takes **work orders, not prompts**. A work order is a small YAML file: the
durable objective, the acceptance criteria that define "done", and the
constraints the work must hold to. Write one, hand it over, and walk away. No
chat loop, no approving each edit: one order in, finished work out. See
[orders.md](orders.md).

## The arcade

**[openzot.github.io/arcade](https://openzot.github.io/arcade/)** - a live,
unattended zot factory you can watch and play.

Every 30 minutes a GitHub Actions job hands zot the same standing order: read
the catalogue of games made so far, invent one that is unlike everything on it,
build it as vanilla HTML, CSS and JavaScript, playtest it in a headless
browser, and add it to the catalogue. The workflow commits whatever zot leaves
in the tree and publishes the site. No pull request, no review, no human in the
loop - the live site *is* the working tree.

It is the shortest honest answer to "what does a fully autonomous run actually
produce?": one order, run on a schedule, and a shelf of finished games nobody
asked for individually. The order, the conventions zot reads before each shift,
and every shift's commit are in
[openzot/arcade](https://github.com/openzot/arcade).

## Status

zot is **0.x**: functional and in active use, with improvements landing release
to release. Until 1.0 the CLI flags, config, and behavior may still change
between versions - pin a version and skim the [changelog](../CHANGELOG.md)
before upgrading.
