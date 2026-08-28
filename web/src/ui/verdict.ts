/**
 * What the room is told about the player who just went
 * (MULTIPLE_IMPOSTERS.md, "Elimination Results").
 *
 * This lives on its own because two things say it: the result screen and the
 * ejection cinematic. Two copies of this wording is one copy too many — the
 * Hidden case is the whole reason the setting exists, and a second version of
 * it that drifted would disclose an alignment the server deliberately withheld.
 *
 * Under Hidden the answer is that there is no answer, and the copy has to say
 * so rather than defaulting to the reassuring half of the truth: a silent
 * "they were not the imposter" would be a lie the setting exists to prevent.
 *
 * Under Reveal with two imposters, catching one does not end the match, so the
 * line cannot promise a group win. It does not count the survivors either: the
 * doc leaves that implied by the public Imposters setting, and a count computed
 * here would be one more thing to get wrong across a reconnect.
 */
export function verdict(imposterCount: number, revealed: boolean, wasImposter: boolean): string {
  const many = imposterCount > 1;
  if (!revealed) {
    return many
      ? "Which side they were on stays hidden. Somebody here is still holding a different word."
      : "Which side they were on stays hidden — that is how this match is set up.";
  }
  if (!wasImposter) {
    return many
      ? "They were not an imposter. Two people here are still holding a different word."
      : "They were not the imposter. Somebody here is still holding a different word.";
  }
  return many
    ? `That was an imposter. The group only wins once all ${imposterCount} are out.`
    : "They were the imposter. The group wins.";
}
