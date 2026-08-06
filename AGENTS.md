# AGENTS.md

zot is an autonomous coding agent in a single Go binary. The engine runs
in-process and talks straight to any OpenAI-compatible model provider - no hosted
service. `agent/` is the public harness; `internal/` holds the engine (loop,
thread assembly, compaction, provider transports, tokenizer, session logs) and
the `tui/` viewer.

## Working here

- **This is 0.x: break things freely.** Breaking changes are expected and welcome
  before 1.0. Backward compatibility is *not* a goal - keeping a deprecated field,
  an alias, a legacy code path, or an old on-disk format "just in case" is an
  anti-pattern here: it adds surface, hides the real design, and there are no
  external users to protect yet. When you rename or replace something, rename or
  replace it everywhere and delete the old thing. Note the break in `CHANGELOG.md`
  rather than carrying it.
- **Test everything you change.** `make test`, and `make cover-check` must pass -
  total coverage may not fall below 90%. Write tests that assert *behaviour*, not
  constants; a test that restates a value it reads is worse than none.
- **Both build variants compile.** `make vet` runs `go vet` over the release and
  `-tags dev` builds. CI also runs `govulncheck`.
- **The tools the model has are `read`, `write`, `list`, `shell`, `plan`,
  `progress`, and the terminal `_success` / `_failure`.** If you change the
  toolset, change `DefaultInstructions` too - a prompt that names a tool that does
  not exist is a bug.
- **Match the surrounding code.** Same naming, comment density, and idioms.
- See `.agents/skills/` for deeper conventions (start with `testing-and-coverage`).
