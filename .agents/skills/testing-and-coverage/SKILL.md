---
name: testing-and-coverage
description: How tests are written and how the 90% coverage gate is enforced in the zot repository. Read this before adding or changing tests, or when the coverage gate fails.
---

# Testing and coverage

The bar in this repo is not "there is a test" - it is "the test would fail if the
behaviour broke." Coverage is gated at 90% so that bar cannot quietly erode.

## The gate

- `make cover-check` (locally) and the **Coverage gate** CI step run the same
  script, `scripts/coverage.sh`. Total statement coverage across the module may
  not fall below **90%**.
- The script prints per-package coverage before the total, so a failure points at
  the package that dropped. Fix it by testing the code you added - not by lowering
  the threshold. `COVERAGE_THRESHOLD=95 make cover-check` raises the bar locally.
- New code without tests is the usual cause of a drop: a handler, a renderer, a
  config field. Coverage falling is the signal that the change shipped untested.

## What a good test looks like here

- **It asserts behaviour, never a constant.** `if MaxOverhead != 10` is not a
  test - change the constant and it "passes" for the wrong reason. Instead drive
  the behaviour the constant produces (`price 200 messages, assert the envelope
  cost`). A test that restates a value it reads is worse than no test, because it
  reads as coverage while guaranteeing nothing.
- **It bites.** Before trusting a new test, break the code it covers and confirm
  the test fails. A green test over broken code is a false negative you have now
  committed. The provider wire tests, the cycle/settle guards, and the coverage
  gate itself were all bite-checked this way.
- **It states the failure it prevents** in a sentence, usually as the comment
  above it - "a result whose call was trimmed away would be rejected", not
  "tests dropOrphaned". The comment is why the test exists; the assertion is how.
- **It is table-driven when the cases are variations** of one shape (see the
  provider and catalogue tests), and a named function when the setup differs.

## Levels, and which to reach for

- **Behavioural** (preferred): drive the real function/loop and assert the
  outcome. `internal/loop/budget_test.go` (a run stops with `StopTime`),
  `internal/provider/*_test.go` (a request carries `max_tokens` on the wire).
- **Wire/serialisation**: for anything that has to reach a provider, assert the
  request body, not just the in-memory struct. Setting a field that never
  serialises is a silent no-op - `max_tokens` had exactly this gap.
- **Integration/threading**: for config, prove the value reaches the run
  (`resolve()` in `zot_test.go`), and that its env override works
  (`internal/config/config_test.go`). A config knob is three links - file, env,
  and the run - and each has broken independently.
- **Construction**: acceptable for defaults that a behavioural test would only
  reach slowly (a 1000-call run to prove "unbounded" is not worth the wall-clock;
  asserting the constructed budget is 0 is).

## Corpus tests (`internal/thread`, `internal/compaction`)

These pin the engine's answers against the implementation zot was ported from.
Do not weaken a corpus expectation to make a change pass. A deliberate divergence
is declared in that package's `divergence_test.go`, with a reason that has to
survive being re-read in a year - and the suite fails if a declaration covers no
record or names a replacement test that does not exist. If your change makes a
corpus record fail, either the change is wrong or the divergence is real and
belongs in that list.

## Running

```bash
make test          # the suite
make race          # under the race detector (CI runs this)
make cover         # per-package coverage, no gate - just the numbers
make cover-check   # the gate: fails below 90%
make vet           # go vet over both the release and -tags dev builds
```
