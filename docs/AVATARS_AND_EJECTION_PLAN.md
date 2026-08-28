# Avatars and Ejection Cinematic

## Goal

Add a fixed roster of 10 original, hand-painted Gestral-like creature portraits, selected before a player enters a room. Add a centered, lightweight petal-dissolve cinematic for in-game vote ejections.

The work must not use Clair Obscur characters, names, distinctive designs, terminology, sound effects, or other protected assets.

## Decided behavior

- Players choose an avatar in the home/join flow, before creating or joining a room.
- The picker is not available in the lobby or during a match.
- Multiple players may choose the same avatar.
- The chosen avatar belongs to the server-owned seat and survives reconnects.
- The player is still identified by their display name; an avatar is decorative reinforcement, not the only identifier.
- Every vote ejection, imposter or not, shows the cinematic to every connected player, including the player who just became a spectator.
- The cinematic uses the room's existing elimination-results mode:
  - **Reveal:** state whether the ejected player was an imposter.
  - **Hidden:** state only that the player was ejected.
- Host/admin kicks remain lobby-only, immediate, and unanimated. They continue to use `ERROR_CODE_KICKED` and remove the seat.
- The ejected player remains a spectator. No vote, phase, timing, reconnect, or spectator-flow rule changes.

## Existing flow to preserve

[`PlayerEliminated`](../proto/verso/v1/game.proto) already broadcasts the eliminated player, the elimination result, and whether alignment is revealable. [`applyElimination`](../internal/room/end.go) marks the player eliminated, broadcasts that result, and sends the player their private spectator dossier.

The cinematic should react to that existing authoritative event. It does not need a separate protocol event, server-side animation timer, or new phase.

[`onKick`](../internal/room/reconnect.go) only operates in the lobby, sends `ERROR_CODE_KICKED`, and removes the target seat. It must remain outside the cinematic trigger path.

The existing `avatar()` helper in [`web/src/ui/avatar.ts`](../web/src/ui/avatar.ts) currently renders player initials against a deterministic color. Replace its visual output centrally so existing roster, vote, result, spectator, and final-reveal callers stay consistent.

## Avatar protocol and room state

1. Add an `Avatar` enum in [`proto/verso/v1/game.proto`](../proto/verso/v1/game.proto), with `AVATAR_UNSPECIFIED = 0` and 10 stable values. Do not renumber or repurpose published values.
2. Add `avatar` to:
   - `JoinRoom`, as the fresh-seat selection;
   - `PlayerInfo`, as the public roster value.
3. Regenerate [`internal/gen/verso/v1/game.pb.go`](../internal/gen/verso/v1/game.pb.go) and [`web/gen/verso/v1/game_pb.ts`](../web/gen/verso/v1/game_pb.ts) with `buf generate`. Do not hand-edit generated files.
4. Add server-side avatar normalization beside existing name/settings validation. Unspecified and unknown enum values must become one designated fallback avatar.
5. Add the avatar to `room.Player`, populate it on a fresh `Seat`, and include it in `Player.Info()`. Thread it through public/private seat APIs, the transport fresh-seat path, registry room creation, and test helpers.
6. On `Attach`/seat-token reclaim, ignore the avatar in the incoming join frame just as the incoming display name is ignored. The existing seat's stored avatar wins.

Adding a new optional protobuf field is backward compatible, so this feature alone does not require a protocol-version bump.

## Original avatar roster

1. Create one avatar-catalog module under `web/src/ui/` that maps each fixed enum value to:
   - optimized image URL;
   - concise accessible label;
   - safe fallback metadata.
2. Add 10 static, optimized portrait assets under the web source tree. Use a shared circular crop and muted painterly palette.
3. Use clearly original creature silhouettes and props, such as a masked beetle, long-eared courier, lantern carrier, tiny gearsmith, mushroom-capped scout, and related fantasy creatures. Do not imitate a named or recognizable Clair Obscur character.
4. Keep only optimized display assets in the shipped app. Do not load or process art dynamically at runtime.
5. Update `avatar()` to render catalog art in the existing small, medium, and large circular containers. Keep adjacent display-name text as the accessible player identity.
6. Pass the player/avatar value through every current `avatar()` caller. Any spectator or final-reveal payload that duplicates player ID/name must also contain the public avatar value so those views can render the correct portrait.

## Join picker

1. Extend [`web/src/ui/screens/home.ts`](../web/src/ui/screens/home.ts) with the required avatar picker after the display-name field and before Create/Join actions.
2. Render the roster as a compact, responsive button grid. Each option must have an image, accessible name, selected state, visible keyboard focus, and a selection mark that is not color-only.
3. Require an avatar choice for both Create and Join while retaining current display-name, room-code, deep-link, and Enter-key validation behavior.
4. Pass selection through:
   - the `Actions` interface in [`web/src/ui/context.ts`](../web/src/ui/context.ts);
   - action wiring in [`web/src/main.ts`](../web/src/main.ts);
   - socket intent/retry state in [`web/src/net/socket.ts`](../web/src/net/socket.ts);
   - `JoinRoom` construction in [`web/src/net/commands.ts`](../web/src/net/commands.ts).
5. Keep the selected value as transient home-screen state until a fresh seat is created. Server-owned `PlayerInfo` becomes the source of truth after entry. A player who was admin-kicked can select again when taking a new seat.
6. Add responsive CSS in the existing component/screen styles. Keep the picker usable on small touch screens without requiring hover.

## Ejection cinematic

1. Create a small app-level cinematic controller/view mounted once beside the screen root from [`web/src/main.ts`](../web/src/main.ts). It must not belong to the result screen, because underlying screens can mount/unmount while the cinematic exists.
2. Observe state updates from [`web/src/state/store.ts`](../web/src/state/store.ts) and trigger once per fresh elimination key when:
   - `elimination.eliminated` is true;
   - the target player resolves from the public roster;
   - the value came from the live `playerEliminated` broadcast, not a snapshot/resync or an `ERROR_CODE_KICKED` reset.
3. Render a centered overlay above a dimmed underlying room:
   - target avatar and display name;
   - short hold;
   - controlled petal burst and dissolve;
   - result copy permitted by `alignmentRevealed` and `wasImposter`.
4. Reuse or extract the authoritative verdict wording from [`web/src/ui/screens/result.ts`](../web/src/ui/screens/result.ts) so Hidden mode cannot accidentally disclose alignment.
5. Target a total duration of about 1.2–1.5 seconds, well inside the current eight-second resolving view. Do not delay `afterResolve`, block snapshot processing, alter phase timers, or change spectator information.
6. Implement the effect with a small, fixed number of DOM or inline-SVG petals and compositor-friendly CSS `transform`/`opacity` keyframes. Avoid per-frame JavaScript, canvas physics, networked particles, layout reads, and unbounded DOM growth.
7. Remove or reset the overlay after it finishes so it cannot intercept later input or reappear on routine store updates.
8. Respect `prefers-reduced-motion`: show a static/fade-only result with no drifting particles. Announce the authorized result once through the existing live-region route, without moving focus.

## Audio

1. Add a quiet, original `ejection` cue to [`web/src/audio/cues.ts`](../web/src/audio/cues.ts), using the existing synthesized pentatonic system rather than a copied sound effect.
2. Extend [`web/src/audio/transitions.ts`](../web/src/audio/transitions.ts) so it derives from the same newly observed vote elimination as the overlay.
3. The cue must be silent for no-elimination results, snapshots, reconnects, and admin kicks. It must obey the existing sound preference and browser audio-unlock behavior.
4. Stage the petal/chime burst after the existing resolving punctuator ends, or coalesce the two cues, so adjacent `PhaseChanged` and `PlayerEliminated` frames do not produce overlapping audio.

## Test and verification plan

1. Add Go coverage for:
   - fresh-seat avatar storage and public roster output;
   - duplicate avatar acceptance;
   - unknown/unspecified fallback;
   - reconnect retaining the original stored avatar despite a conflicting incoming value;
   - existing admin-kick behavior still emitting only `ERROR_CODE_KICKED`, with no elimination broadcast.
2. Update room harnesses, registry/transport tests, and all direct `Seat` callers for the new avatar parameter.
3. Keep avatar catalog and cinematic trigger logic pure where practical. Add client tests if the project gains a test runner; otherwise cover them through strict type checking and focused manual verification.
4. Manually validate 3–10-player rooms:
   - all screens show the selected portrait;
   - duplicate portraits work;
   - reconnect keeps the selected portrait;
   - Reveal and Hidden ejections show only allowed text;
   - terminal and non-terminal ejections retain the current result/spectator flows;
   - an admin lobby kick is still immediate and unanimated.
5. Run:
   - `make gen`
   - `make test`
   - `make build`
6. Profile the cinematic in browser performance tooling. It should be a short compositor-only animation with no sustained main-thread work and no interaction-frame impact after completion.

## Out of scope

- User-uploaded avatars, account-level persistence, cosmetics, avatar exclusivity, or editing an avatar in the lobby/mid-match.
- Vote-rule, ejection-timing, phase, spectator-permission, or admin-kick changes.
- Any reproduction of Clair Obscur artwork, characters, terminology, visual assets, or audio.
