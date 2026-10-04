// Package plan owns the provider layout PLAN-FIELD table for resolved provider inputs.
// It is distinct from workspace.Plan or a launch plan. This is the final home:
// the legacy adapters/layout table will be removed while this package stays.
// The root retains the generator and generated exports and may re-export.
// The new table has no generated export yet. Existing root exports describe
// the legacy table until cutover.
// It is pure: no writes, credential reads, pin resolution or process launches.
// The parent layout package temporarily retains its legacy table to preserve
// existing projections. Cutover follows serializer and workspace integration;
// the planned removal of the legacy table follows that cutover.
//
// Intended deltas from legacy output, applied only at cutover:
//   - Codex boot skills use skills, rather than Cairn's .agents/skills, because
//     the measured --cd probe loses the latter. Installed skills retain .agents/skills.
//   - OpenCode instructions use agents/{agent}.md plus opencode.json.
//   - Claude settings.json and .mcp.json are explicitly 0600.
//   - Codex config.toml and Antigravity mcp_config.json are explicitly 0600;
//     Codex auth.json is a credential link effect, never an empty planted file.
//   - Unread MCP mirrors, OpenCode agents.json and .opencode/skills are dropped.
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
// Skill support files retain their declared modes; filenames never select modes.
// Commands, subagents and prompts are pinned content registrations, not new
// agentdef fields or extensions.
package plan
