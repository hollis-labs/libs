---
id: fixture
name: Fixture
provider: claude
spec:
  skills_dir: $CAIRN_PROFILE_ROOT/skills
  prompts_dir: $CAIRN_PROFILE_ROOT/prompts
  skills: [sample]
  prompts: [handoff]
  subagents: [reviewer]
  templates:
    AGENTS.md: "Fixture instructions.\n"
    CLAUDE.md: "@AGENTS.md\n"
  settings:
    claude:
      permissions: { defaultMode: acceptEdits }
    codex:
      approval_policy: on-request
  mcp:
    - name: fixture
      command: fixture-server
      args: [one, two]
  files:
    notes/empty.txt: ""
---
