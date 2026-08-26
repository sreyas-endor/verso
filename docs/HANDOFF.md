# Verso — handoff

**Stopped:** 2026-08-26, mid-build, deliberately.
**Reason:** the build workflow was halted after the Client phase; remaining phases move to another tool.

## Where it stopped

A 15-agent workflow was running the milestones in `IMPLEMENTATION_PLAN.md` §6. It completed 12 of 15 agents and was killed during the Client phase.

| Phase | Agents | Status |
|---|---:|---|
| Protocol — `game.proto`, codegen, room API contract | 1 | done |
| Core — room actor, transport+registry+main, word decks | 3 | done |
| Compile — build/vet clean, seam audit | 1 | done (one crash + auto-retry) |
| Tests — synctest phase machine, rules, canary, strokes | 2 | done |
| Bots — headless protocol clients | 1 | done |
| **Client — net/state, canvas, UI screens** | 3 | **killed late; output is coherent, see below** |
| Wire — `index.html`, `main.ts`, embed, Makefile, README | 1 | **never ran** |
| Audit — secret-leak, spec conformance | 2 | **never ran** |
| Fix — apply findings | 1 | **never ran** |

## Current state

```
go build ./...      exit 0
go vet ./...        exit 0
go test ./...       exit 1   — 2 failures, one root cause (see Bug 1)
tsc --noEmit        exit 1   — 1 error (see Bug 2)
```

| | |
|---|---:|
| Go, excluding generated | 16,887 lines |
| of which tests | 8,688 lines |
| TypeScript, incl. generated | 7,267 lines |
| CSS | 469 lines |
| `game.proto` | 540 lines |
| Test functions | 141 |
| Word pairs | 134 |

Green: `internal/registry`, `internal/transport`, `internal/words`.
Red: `internal/room`, `cmd/bot`.

## Open bugs

### Bug 1 — the match plays past its configured final round (real, blocking)

Violates `DESIGN.md` max-rounds (1–4). Found independently by two agents, which is why it is worth trusting.

- `internal/room/rules_test.go:1140` — `RoundStarted announced round 3 of 2`
- `cmd/bot/driver_test.go:361` — `RoundStarted announced round 2 of 1`

The unit test names the cause directly:

> the final-round check sits under `evaluateEnd`'s dark-imposter guard, and `afterResolve` has no cap of its own

So `afterResolve` starts another round without consulting `max_rounds` whenever `evaluateEnd`'s guard short-circuits. Fix in `internal/room/end.go` / `phase.go`; both tests above already encode the correct behaviour and should go green without being touched.

### Bug 2 — duplicate object key

`web/src/state/store.ts:302` — `TS2783: 'youAreEliminated' is specified more than once`. One-line dedup.

## Not yet written (the Wire phase)

- `web/index.html`
- `web/src/main.ts` — font import, boot socket, mount router, wire canvas into the drawing screen
- `Makefile` — `build` (npm build → go build), `dev`, `test`, `gen`
- `README.md`
- `cmd/verso/static.go` currently embeds a placeholder `cmd/verso/dist/index.html`; it must be pointed at the real `web/dist` output

The three Client agents worked blind to each other, so their APIs will disagree at the seams. Reconciling those is the first task, and it is exactly what the Wire phase was scoped to do. `web/src` does typecheck apart from Bug 2, so the disagreements are structural, not syntactic.

## Never ran: the two audits

Nothing has independently verified:

- the secret-leak defense end to end, including whether the browser ever *holds* another player's word even if it does not render it (a word the client has but hides is still a leak)
- line-by-line conformance to `DESIGN.md`
- that no `TODO`/stub/fake-work survived

The canary test (`internal/room/canary_test.go`, 853 lines) does pass and does check serialized bytes rather than struct fields, including the sentinel split across adjacent length-prefixed fields. That is evidence, not a substitute for the audit.

## Rules change made after the agents were briefed

The strict-majority denominator was changed mid-run. `DESIGN.md` now defines it normatively under "Active players"; `IMPLEMENTATION_PLAN.md:248` and open-question 3 record the decision.

**Active = connected AND not eliminated.** A disconnected player is excluded from the majority denominator, the turn order, and the two-players-remain count. Seat, word and match state are still retained for reconnect.

**The Go code still implements the old rule** — disconnected players counted in the denominator with a `Skip` vote. The agents were briefed before the change. Needs:

1. `internal/room/vote.go` — compute the denominator from currently-connected, non-eliminated players
2. the majority tests in `internal/room/rules_test.go` — they currently assert the old rule
3. turn order and the two-players-remain check in `internal/room/end.go`

Still undecided: whether the denominator is **frozen when discussion opens** or **recomputed at tally time**. Recomputing live means a disconnect can retroactively push an already-stalled vote over the line — drop one player from 6 and three existing votes become a majority. Freezing avoids that but lets a departed player block elimination for the rest of the phase.

## How to run things

```sh
go build ./... && go vet ./... && go test ./... -count=1
go test ./... -race                      # not yet verified clean
cd web && npm run typecheck && npm run build
buf lint && buf generate                 # after editing the .proto
```

Toolchain: Go resolves to **1.26.6** automatically via `GOTOOLCHAIN=auto` (`go.mod` sets the floor; `IMPLEMENTATION_PLAN.md` §3 explains why 1.26.5 is not enough). `buf` and `protoc-gen-go` are in `~/go/bin`; `protoc-gen-es` is in `web/node_modules`.

## Suggested order

1. Bug 1 — it is a genuine rules violation and both tests are already written
2. The denominator change, and decide frozen vs. live first
3. Bug 2, then the Wire phase, so the thing actually boots in a browser
4. The two audits, last, once there is something whole to audit
