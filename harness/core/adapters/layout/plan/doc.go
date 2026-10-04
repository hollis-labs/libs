// Package plan owns the provider layout PLAN-FIELD table for resolved provider inputs.
// It is distinct from workspace.Plan or a launch plan. This is the final home:
// the legacy adapters/layout table will be removed while this package stays.
// The root retains the generator and generated exports and may re-export.
// The plan-fields.json and docs/PLAN-FIELDS.md exports describe this table.
// Existing layout.json and docs/LAYOUT.md exports describe the legacy table
// until cutover.
// It is pure: no writes, credential reads, pin resolution or process launches.
// The parent layout package temporarily retains its legacy table to preserve
// existing projections. Cutover follows serializer and workspace integration;
// the planned removal of the legacy table follows that cutover.
//
// Intended deltas from legacy output, applied only at cutover:
//   - Absent Codex posture drops implicit never/workspace-write native defaults.
//     Headless operation then uses runtime defaults; a declared host default
//     can explicitly retain the former native settings. Render diagnoses absence.
//   - Explicit postures add Claude defaultMode, posture-derived Codex approval
//     and sandbox settings, and evidenced OpenCode permission config. The bound
//     mode also drives the runtime reference; Antigravity diagnoses native absence.
//   - Boot instruction bodies are the resolved definition text: Codex and Antigravity
//     drop synthetic YAML front matter and H1 titles; Claude, Codex and Antigravity
//     drop synthetic MCP sections and endpoint URLs from instruction documents.
//   - OpenCode keeps its native primary-agent front matter and drops the
//     synthetic H1 title and MCP endpoint tail from the resolved prompt body.
//   - MCP server names have no reserved names; a server named mux has the same
//     environment-omission behavior as every other stdio server.
//   - Native directory grants reject relative and traversal-bearing paths.
//   - Codex boot skills use skills, rather than Cairn's .agents/skills, because
//     the measured --cd probe loses the latter. Installed skills retain .agents/skills.
//   - OpenCode instructions use agents/{agent}.md plus opencode.json.
//   - Claude settings.json and .mcp.json are explicitly 0600.
//   - Codex config.toml and Antigravity mcp_config.json are explicitly 0600;
//     Codex auth.json is a credential link effect, never an empty planted file.
//   - Unread MCP mirrors, OpenCode agents.json and .opencode/skills are dropped.
//
// Installed encodings retain the generated-by banner (the resolved definition
// identifier replaces the profile identifier) and Codex literal-string/parent
// MCP tables so human-run drift checks retain their content semantics. Installed
// Claude settings narrow 0644 to 0600 at explicit installed apply; no mode is
// widened. Existing-document reads and recursive owned-key merge belong to that
// apply edge, not render; render supplies the leaf-key ownership contract.
//
// OpenCode and Antigravity installation are explicitly unsupported. Follow-up: design and
// measure an authorized installed-layer layout before enabling it, as for
// deferred ACP and Gemini support. Boot paths never imply home installation.
//
// Credential destinations are LINK-ONLY / NEVER-WRITE. Resolve returns binding
// effects rather than regular-file rows; replant preserves them untouched.
// Claude credential and Antigravity Keychain availability remain external runtime
// preconditions: this table invents no credential file destination for either.
// Workspace apply owns authorized initial linking and overwrite refusal.
//
// Files default to 0644, directories to 0755 inside a private 0700 boot root.
// Pinned skill entries, including SKILL.md, preserve declared modes after
// removing special bits and group/other write. Changed modes produce a named
// diagnostic. Absent modes use the row default; filenames never add executability.
// Commands, subagents and prompts are pinned content registrations, not new
// agentdef fields or extensions.
package plan
