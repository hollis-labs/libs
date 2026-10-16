package catalog

import (
	"embed"
	"fmt"
)

//go:embed tools/*.schema.json
var toolFS embed.FS

// ToolSchema returns the JSON Schema for an agent self-report contract file
// in tools/, named without its .schema.json suffix, for example
// "torque_task_progress.input@1" or "progress_detail@1.setting". See
// selfreport.md.
func ToolSchema(name string) ([]byte, error) {
	b, err := toolFS.ReadFile("tools/" + name + ".schema.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: unknown tool schema %q", name)
	}
	return b, nil
}

// ProgressDetail is the progress_detail setting.
type ProgressDetail string

// Progress detail levels, from least to most detailed.
const (
	DetailOff       ProgressDetail = "off"
	DetailMilestone ProgressDetail = "milestone"
	DetailStep      ProgressDetail = "step"
	DetailVerbose   ProgressDetail = "verbose"
)

// DefaultProgressDetail applies when no launch, task or template sets one.
const DefaultProgressDetail = DetailMilestone

// Sources of the effective progress_detail, stamped on progress events as
// detail_source.
const (
	SourceLaunch   = "launch"
	SourceTask     = "task"
	SourceTemplate = "template"
	SourceDefault  = "default"
)

func (d ProgressDetail) rank() int {
	switch d {
	case DetailOff:
		return 0
	case DetailMilestone:
		return 1
	case DetailStep:
		return 2
	case DetailVerbose:
		return 3
	}
	return -1
}

// Valid reports whether d is one of the four levels.
func (d ProgressDetail) Valid() bool { return d.rank() >= 0 }

// EffectiveProgressDetail resolves the setting: the first valid value of
// launch, task and template wins, otherwise [DefaultProgressDetail]. An
// empty or unknown value is skipped, so a typo never silences progress.
//
// In Torque, template values are copied onto the task at creation with their
// origin recorded. Pass the task's value as task, or as template when its
// recorded origin is template. launch is the value stored on the run or
// session launch request, never on a launch_profile.
func EffectiveProgressDetail(launch, task, template ProgressDetail) (ProgressDetail, string) {
	for _, c := range []struct {
		d   ProgressDetail
		src string
	}{{launch, SourceLaunch}, {task, SourceTask}, {template, SourceTemplate}} {
		if c.d.Valid() {
			return c.d, c.src
		}
	}
	return DefaultProgressDetail, SourceDefault
}

// Allows reports whether a report at level passes the effective setting.
// off allows nothing; an invalid level is never allowed.
func (effective ProgressDetail) Allows(level ProgressDetail) bool {
	if level == DetailOff || !level.Valid() || !effective.Valid() {
		return false
	}
	return level.rank() <= effective.rank()
}
