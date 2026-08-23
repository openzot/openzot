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
