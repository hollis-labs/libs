// Package agentdef parses, validates, fingerprints and layers v1 agent
// definition files: one markdown file per agent, YAML frontmatter above the
// instructions.
//
// The frontmatter is small and strict. name and description are required;
// title, identity, skills, tools, requires, uses, hooks, icon, avatar, tags
// and metadata are optional; any other key is an error. A definition holds no
// provider, model, runtime, launch or grant field — those belong to the host.
//
// The flow is Parse (or ParseFile), Validate, then Digest. Digest is a
// sha256 over a canonical JSON form that ignores where the file came from.
// LoadLayers merges several directory roots with explicit precedence and
// reports same-name collisions instead of guessing. ResolveSkills finds and
// hash-pins the skills a definition names, following the Agent Skills layout
// (skills/<name>/SKILL.md). CheckGenerated detects stale text that was
// generated into a body at authoring time.
//
// The package never composes or resolves definitions at runtime, never
// touches git, and reads only the fs.FS it is handed. The agentdef command
// (cmd/agentdef) exposes the same checks for CI.
package agentdef
