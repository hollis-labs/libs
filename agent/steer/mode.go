package steer

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/hollis-labs/libs/core/util/router"
)

// Preset is a named bundle of behaviour. A mode is a preset the owner chose.
type Preset struct {
	// Name is the preset's name: lower case letters, digits, dots, dashes, underscores.
	Name string `json:"name"`
	// About says what the preset is for, for the owner.
	About string `json:"about,omitempty"`
	// Slots are slots, by name, made active whatever is in front.
	Slots []string `json:"slots,omitempty"`
	// Instructions are standing guidance while the preset is in force.
	Instructions string `json:"instructions,omitempty"`
	// Reflexes are reflexes, by name, switched on while the preset is in force.
	Reflexes []string `json:"reflexes,omitempty"`
	// Rules are routing rules, by id, switched on while the preset is in force.
	Rules []string `json:"rules,omitempty"`
	// Tools are tools to keep top of mind, by name.
	Tools []string `json:"tools,omitempty"`
	// OutputStyle names how replies are written ("brief", "detailed"); the host interprets it.
	OutputStyle string `json:"output_style,omitempty"`
	// Delivery names the delivery policy that paces output; the host interprets it.
	Delivery string `json:"delivery,omitempty"`
	// Dispatch lists the dispatch strategies allowed ("subtask", "council", "seat"). Empty
	// leaves the host's default.
	Dispatch []string `json:"dispatch,omitempty"`
}

var presetName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate checks a preset's name.
func (p Preset) Validate() error {
	if !presetName.MatchString(p.Name) {
		return fmt.Errorf("preset name %q: use lower case letters, digits, dots, dashes and underscores", p.Name)
	}
	return nil
}

// Layer is where a preset came from. A higher layer replaces a lower one of the same name:
// app > module > default.
type Layer int

const (
	// LayerDefault is the presets this package ships.
	LayerDefault Layer = iota
	// LayerModule is presets a module contributes.
	LayerModule
	// LayerApp is the app's own presets. They win.
	LayerApp
)

func (l Layer) String() string {
	switch l {
	case LayerDefault:
		return "default"
	case LayerModule:
		return "module"
	case LayerApp:
		return "app"
	}
	return fmt.Sprintf("layer(%d)", int(l))
}

// MarshalText writes the layer's name.
func (l Layer) MarshalText() ([]byte, error) {
	switch l {
	case LayerDefault, LayerModule, LayerApp:
		return []byte(l.String()), nil
	}
	return nil, fmt.Errorf("unknown layer %d", int(l))
}

// UnmarshalText reads a layer's name.
func (l *Layer) UnmarshalText(b []byte) error {
	switch string(b) {
	case "default":
		*l = LayerDefault
	case "module":
		*l = LayerModule
	case "app":
		*l = LayerApp
	default:
		return fmt.Errorf("unknown layer %q: use default, module or app", b)
	}
	return nil
}

// Origin says where an effective preset came from.
type Origin struct {
	Layer Layer `json:"layer"`
	// Source names the contributor within the layer: the module, or "" for the app and the
	// defaults.
	Source string `json:"source,omitempty"`
}

type entry struct {
	preset Preset
	origin Origin
}

// Registry holds presets in layers. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	entries map[string][]entry
	deflt   string
}

// ErrUnknownPreset is returned for a preset name the registry does not hold.
var ErrUnknownPreset = errors.New("no such preset")

// NewRegistry returns a registry holding the given default presets, which must have
// distinct names. The first is the default, used when no mode is set and nothing is
// classified; with none, it is empty.
func NewRegistry(defaults ...Preset) (*Registry, error) {
	r := &Registry{entries: map[string][]entry{}}
	for _, p := range defaults {
		if _, ok := r.entries[p.Name]; ok {
			return nil, fmt.Errorf("default preset %s is listed twice", p.Name)
		}
		if err := r.Add(LayerDefault, "", p); err != nil {
			return nil, err
		}
	}
	if len(defaults) > 0 {
		r.deflt = defaults[0].Name
	}
	return r, nil
}

// Add puts a preset in a layer. A module preset names its module in source. Adding a preset
// the same layer and source already holds replaces it.
func (r *Registry) Add(layer Layer, source string, p Preset) error {
	if err := p.Validate(); err != nil {
		return err
	}
	switch layer {
	case LayerDefault, LayerApp:
		if source != "" {
			return fmt.Errorf("preset %s: only a module preset names a source", p.Name)
		}
	case LayerModule:
		if source == "" {
			return fmt.Errorf("preset %s: a module preset names its module", p.Name)
		}
	default:
		return fmt.Errorf("preset %s: unknown layer %d", p.Name, int(layer))
	}
	o := Origin{Layer: layer, Source: source}
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.entries[p.Name]
	for i, e := range list {
		if e.origin == o {
			list[i] = entry{preset: clonePreset(p), origin: o}
			return nil
		}
	}
	r.entries[p.Name] = append(list, entry{preset: clonePreset(p), origin: o})
	return nil
}

// Remove takes out what a layer and source contributed under name, if anything. It refuses
// to remove the last definition of the default preset.
func (r *Registry) Remove(layer Layer, source, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := slices.DeleteFunc(slices.Clone(r.entries[name]), func(e entry) bool { return e.origin == Origin{Layer: layer, Source: source} })
	if len(list) == 0 {
		if name == r.deflt {
			return fmt.Errorf("preset %s is the default; set another default first", name)
		}
		delete(r.entries, name)
		return nil
	}
	r.entries[name] = list
	return nil
}

// SetDefault names the preset used when no mode is set and nothing is classified.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[name]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPreset, name)
	}
	r.deflt = name
	return nil
}

// Default is the default preset's name.
func (r *Registry) Default() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deflt
}

// Get returns the effective preset called name: the one in the highest layer. Two modules
// contributing the same name in the module layer: the one added first wins.
func (r *Registry) Get(name string) (Preset, Origin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.entries[name]
	if len(list) == 0 {
		return Preset{}, Origin{}, false
	}
	best := list[0]
	for _, e := range list[1:] {
		if e.origin.Layer > best.origin.Layer {
			best = e
		}
	}
	return clonePreset(best.preset), best.origin, true
}

// Names lists the effective presets' names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for n := range r.entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Check reports every effective preset's reference to a slot, reflex or routing rule the
// host does not have. A host calls it when its configuration changes; nil means all
// references resolve.
func (r *Registry) Check(slots []Slot, reflexes []Reflex, rules router.RuleSet) []error {
	var errs []error
	for _, name := range r.Names() {
		p, _, _ := r.Get(name)
		for _, s := range p.Slots {
			if _, ok := Named(slots, s); !ok {
				errs = append(errs, fmt.Errorf("preset %s: no slot %q", name, s))
			}
		}
		for _, x := range p.Reflexes {
			if !slices.ContainsFunc(reflexes, func(r Reflex) bool { return r.Name == x }) {
				errs = append(errs, fmt.Errorf("preset %s: no reflex %q", name, x))
			}
		}
		for _, id := range p.Rules {
			if !slices.ContainsFunc(rules.Rules, func(r router.Rule) bool { return r.ID == id }) {
				errs = append(errs, fmt.Errorf("preset %s: no routing rule %q", name, id))
			}
		}
	}
	return errs
}

func clonePreset(p Preset) Preset {
	p.Slots = slices.Clone(p.Slots)
	p.Reflexes = slices.Clone(p.Reflexes)
	p.Rules = slices.Clone(p.Rules)
	p.Tools = slices.Clone(p.Tools)
	p.Dispatch = slices.Clone(p.Dispatch)
	return p
}

// DefaultPresets are the presets this package ships. "general" is the default.
func DefaultPresets() []Preset {
	return []Preset{
		{Name: "general", About: "Whatever is in front of the owner."},
		{Name: "focus", About: "Deep work. Hold anything that is not urgent.",
			Instructions: "The owner is concentrating. Keep replies short and do not raise new topics unless asked.",
			OutputStyle:  "brief", Delivery: "hold"},
		{Name: "triage", About: "Going through what is waiting.",
			Instructions: "Help the owner sort what is waiting: lead with what needs a decision, then what can wait.",
			OutputStyle:  "brief", Delivery: "digest"},
		{Name: "research", About: "Finding things out.",
			Instructions: "Cite where each finding comes from. Say what is uncertain.",
			Dispatch:     []string{"subtask"}},
	}
}

// Modes holds the deliberate mode: the one the owner set. Reading it is open; setting it
// goes only through the [OwnerControls] that [New] hands the host, so an agent tool, a
// reflex or anything decoded from input cannot set it.
type Modes struct {
	reg *Registry
	now func() time.Time

	mu    sync.Mutex
	name  string
	since time.Time
}

// ModeState is the deliberate mode and when it was set; Name is "" when none is set.
type ModeState struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
}

// Current is the deliberate mode, "" when none is set.
func (m *Modes) Current() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.name
}

// State is the deliberate mode and when it was set.
func (m *Modes) State() ModeState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ModeState{Name: m.name, Since: m.since}
}

// set sets the deliberate mode; "" clears it.
func (m *Modes) set(name string) error {
	if name != "" {
		if _, _, ok := m.reg.Get(name); !ok {
			return fmt.Errorf("%w: %s", ErrUnknownPreset, name)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name, m.since = name, m.now()
	return nil
}

// Source of a resolved preset.
const (
	SourceDeliberate = "deliberate"
	SourceClassified = "classified"
	SourceDefault    = "default"
)

// Resolution is the preset in force for a turn and how it was chosen.
type Resolution struct {
	Preset Preset `json:"preset"`
	Origin Origin `json:"origin"`
	// Source is "deliberate", "classified" or "default".
	Source string `json:"source"`
	// Verdict is the policy's ruling on the classified preset, if one was classified.
	Verdict *Verdict `json:"verdict,omitempty"`
}

// Resolve picks the preset in force: a deliberate mode, else a classified preset the policy
// applies, else the default. A deliberate mode that is no longer registered falls through,
// as if none were set. A classified preset that is not registered is recorded and ignored.
func Resolve(reg *Registry, deliberate string, cls Classification, policy Policy) Resolution {
	var verdict *Verdict
	if r, ok := cls[FacetPreset]; ok {
		v := policy.Judge(FacetPreset, r)
		if v.Outcome != OutcomeRecord {
			if _, _, known := reg.Get(r.Label); !known {
				v = v.overrule("unknown label")
			}
		}
		verdict = &v
	}
	if deliberate != "" {
		if p, o, ok := reg.Get(deliberate); ok {
			if verdict != nil {
				v := verdict.overrule("deliberate mode")
				verdict = &v
			}
			return Resolution{Preset: p, Origin: o, Source: SourceDeliberate, Verdict: verdict}
		}
	}
	if verdict != nil && verdict.Outcome == OutcomeApply {
		p, o, _ := reg.Get(verdict.Label)
		return Resolution{Preset: p, Origin: o, Source: SourceClassified, Verdict: verdict}
	}
	p, o, _ := reg.Get(reg.Default())
	return Resolution{Preset: p, Origin: o, Source: SourceDefault, Verdict: verdict}
}
