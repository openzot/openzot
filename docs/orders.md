# Work orders

zot takes **work orders, not prompts**. A work order is a small YAML file: the
durable objective, the acceptance criteria that define "done", and the
constraints the work must hold to. Write one, hand it over, and walk away:

```bash
zot new "add rate limiting to the API, with tests"
# wrote .zot/orders/add-rate-limiting-to-the-api-with-tests.yaml - edit its
# acceptance criteria, then:
zot
```

zot reads the repo, writes and edits the code, runs the build and the tests, and
fixes what it broke - on its own, from that one order to a recorded outcome. When
it stops you have a changed working tree and a full session log of every step it
took. No chat loop, no approving each edit: one order in, finished work out.

## Writing one

`zot new` scaffolds at three levels of help, all landing in the same reviewable
file:

| Command                        | What you get                                                   |
| ------------------------------ | -------------------------------------------------------------- |
| `zot new`                      | the blank form                                                 |
| `zot new "objective"`          | the objective filled in                                        |
| `zot new --draft "objective"`  | the model proposes acceptance criteria and constraints as well |

A draft is a draft for you to edit, never a contract the model signed itself.
Nor is it a blind guess: it runs as a small read-only run through the same
engine, surveying the working tree - the build files, the test setup - so the
criteria it proposes name the project's real commands and files.

An order may declare an optional `title:` - a short label for people, shown in
the viewer instead of the order text. The title never reaches the agent: the
objective is the contract.

## Orders compose

Orders are files, so they compose. A bare `zot` runs the project's whole book -
each order as its own run, in sequence, stopping at the first that does not end
in success - and the ledger skips the ones already done, so you write an order
and type `zot` again. Naming order files runs exactly those, from any path:

```bash
zot                                   # the project's outstanding orders
zot ~/briefs/add-rate-limiting.yaml   # exactly this one
zot ~/briefs/*.yaml                   # exactly these
```

A project's orders and the receipts of what has been run from them live
together under `.zot/`, and both halves point wherever you like - see
[the book](configuration.md#the-book).

## Orders persist

Orders are files, and files belong in repositories. Committing an order makes
it part of the project: reviewable in a pull request, versioned with the code
it asks for, and there for whoever - or whatever - runs it next. The ledger
decides what happens then, and there are two honest ways to set it up:

- **A committed one-shot order** is a to-do with a receipt. Commit `.zot/`
  (orders and records together) and a satisfied order is skipped by every
  future `zot`, on every machine - "already done" travels with the repository.
  Edit the order and its changed content re-queues, like everything in the
  book.
- **A standing order** is the opposite: one committed file meant to run
  forever, each run producing new work from the same words. Doneness is the
  one thing it must never acquire, so run it with `--rerun` (which ignores
  settled records) and keep `.zot/` out of the repository (`.gitignore` it) so
  no receipt can retire it. What carries from run to run is not the ledger but
  whatever the order tells the agent to read first - a catalogue, a log of
  previous passes - which is how each run knows what the last one did.

The [factories](https://github.com/openzot) are standing orders in public:
[the arcade](https://github.com/openzot/arcade)'s `orders/new-game.yaml` makes
a new browser game every shift, [the
machinery](https://github.com/openzot/machinery)'s builds a new machine, and
[the whetstone](https://github.com/openzot/whetstone)'s hones the same game
version after version. Each is a small YAML file committed at the root of its
repository, handed to zot every 30 minutes by a cron with `rerun` set - the
whole factory is the order, the conventions file beside it, and a gate script.

Persisted orders run together like any others: `zot orders/*.yaml` runs
exactly those, in filename order, and a bare `zot` runs the project's whole
book - so a repository can carry several standing orders, or a standing order
alongside one-shot work, and one invocation carries out whatever is
outstanding (with `--rerun` making the standing ones run regardless). Running
zot from CI works the same way; the
[openzot/actions](https://github.com/openzot/actions) action wraps exactly
this, with `orders:` and `rerun:` inputs.

## Orders stream

`zot --watch` keeps zot up and runs every `*.yaml` order that lands in the
folder as it arrives - a drop-box factory:

```bash
zot --watch            # this project's own orders
zot --watch ~/inbox    # any folder, or a glob
```

See [watch mode](configuration.md#watch-mode).

## After the run

Every run is written to disk as it happens, so a run nobody watched is still
answerable afterwards, and can be picked up again with `zot --resume last`. See
[sessions](configuration.md#sessions).
