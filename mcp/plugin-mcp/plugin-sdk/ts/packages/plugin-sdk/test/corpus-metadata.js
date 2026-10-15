// Availability and obligation are separate; malformed waivers never hide a case.
export function transcriptMetadata(fixture) {
  if (!["observed", "proposed"].includes(fixture.status ?? "observed"))
    throw new Error("invalid corpus status");
  if (
    !["normative", "observed-quirk"].includes(fixture.level) ||
    !Array.isArray(fixture.steps)
  )
    throw new Error("invalid corpus level/steps");
  if (
    fixture.status === "proposed" &&
    (!fixture.finding?.trim() || !fixture.unavailable_owner?.trim())
  )
    throw new Error("proposed case needs finding and unavailable_owner");
  return fixture.steps.map((step) => {
    const level = step.level ?? fixture.level;
    if (!["normative", "observed-quirk"].includes(level))
      throw new Error("invalid step level");
    if (
      level === "observed-quirk" &&
      !(step.preferred ?? fixture.preferred)?.trim()
    )
      throw new Error("quirk needs preferred note");
    return level;
  });
}
