package steer

// Focus is the window in front.
type Focus struct {
	// View is the view the window shows ("notes", "notes.list").
	View string `json:"view,omitempty"`
	// Module is the module that owns the view.
	Module string `json:"module,omitempty"`
	// Title is the window's title.
	Title string `json:"title,omitempty"`
	// Selection is the text the owner has selected in it, if any.
	Selection string `json:"selection,omitempty"`
}

// Window is one open window.
type Window struct {
	ID     string `json:"id,omitempty"`
	View   string `json:"view,omitempty"`
	Module string `json:"module,omitempty"`
	Title  string `json:"title,omitempty"`
}

// Situation is what is happening when a turn is prepared. The host fills it.
type Situation struct {
	// Focused is the window in front, when there is one.
	Focused *Focus `json:"focused,omitempty"`
	// Windows are all the open windows.
	Windows []Window `json:"windows,omitempty"`
	// Mode is the mode in force for this turn. [Steer.Prepare] sets it from the resolved
	// preset; a host does not need to.
	Mode string `json:"mode,omitempty"`
	// Layout is the saved layout in use, when there is one.
	Layout string `json:"layout,omitempty"`
	// Input is the owner's message.
	Input string `json:"input,omitempty"`
	// Source says what started the turn ("user", "notice", "schedule").
	Source string `json:"source,omitempty"`
}

// Slot is what the agent brings to one kind of work: the tools top of mind there, standing
// instructions, and where the state of that work is kept.
type Slot struct {
	// Name is unique in the app.
	Name string `json:"name"`
	// About is the kind of work, as the agent is told it.
	About string `json:"about,omitempty"`
	// Module is the module that contributed the slot, if any.
	Module string `json:"module,omitempty"`
	// Views are the views whose window in front selects this slot.
	Views []string `json:"views,omitempty"`
	// Modes are modes that select this slot whatever is in front.
	Modes []string `json:"modes,omitempty"`
	// Tools are top of mind here, by name. The agent's other tools stay available.
	Tools []string `json:"tools,omitempty"`
	// Instructions are standing guidance for this kind of work.
	Instructions string `json:"instructions,omitempty"`
	// Where says where the state of this kind of work is kept.
	Where string `json:"where,omitempty"`
}

// Selection is the slots active for a turn, the first being the primary one.
type Selection struct {
	Slots []Slot `json:"slots"`
	// Why says how they were chosen: "focus", "mode", "preset", "pinned", "kept",
	// "classified" or "default".
	Why string `json:"why"`
}

// Primary is the first active slot, or the zero Slot when there is none.
func (s Selection) Primary() Slot {
	if len(s.Slots) == 0 {
		return Slot{}
	}
	return s.Slots[0]
}

// Names lists the active slots' names.
func (s Selection) Names() []string {
	out := make([]string, 0, len(s.Slots))
	for _, sl := range s.Slots {
		out = append(out, sl.Name)
	}
	return out
}

// has reports whether a slot of that name is selected.
func (s Selection) has(name string) bool {
	for _, sl := range s.Slots {
		if sl.Name == name {
			return true
		}
	}
	return false
}

// Named returns the slot called name among all.
func Named(all []Slot, name string) (Slot, bool) {
	for _, s := range all {
		if s.Name == name {
			return s, true
		}
	}
	return Slot{}, false
}
