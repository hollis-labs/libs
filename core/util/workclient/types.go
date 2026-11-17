package workclient

// The Work service's JSON. Field names and tags follow the service exactly; a field the
// service adds later is dropped until this package adds it.

// Workspace is one workspace in a listing, and the workspace inside a Space.
type Workspace struct {
	Archived bool     `json:"archived"`
	Deleted  bool     `json:"deleted"`
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	App      string   `json:"app"`
	Preset   string   `json:"preset"`
	NS       string   `json:"ns"`
	Owners   []string `json:"owners"`
	Created  string   `json:"created,omitempty"`
	Body     string   `json:"body"`
	Items    int      `json:"items"`
	Rev      int      `json:"rev"`
	Updated  string   `json:"updated"`
	Problem  string   `json:"problem,omitempty"`
}

// Preset is a starting kind set a workspace is made from.
type Preset struct {
	ID        string         `json:"id"`
	App       string         `json:"app"`
	Label     string         `json:"label"`
	Kinds     []Kind         `json:"kinds"`
	Views     []View         `json:"views"`
	Templates []TemplateInfo `json:"templates,omitempty"`
}

// TemplateInfo names a starting tree a preset offers.
type TemplateInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	About string `json:"about"`
}

// Field is one typed field of a kind.
type Field struct {
	Key     string   `json:"key"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type"` // text | number | date | enum | ref | refs | url | bool
	Options []string `json:"options,omitempty"`
}

// Kind is an item kind: its stages, fields, sections and allowed children.
type Kind struct {
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Plural   string   `json:"plural,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	Stages   []string `json:"stages"`
	Fields   []Field  `json:"fields"`
	Sections []string `json:"sections"`
	Children []string `json:"children"`
	Color    string   `json:"color,omitempty"`
	Rev      int      `json:"rev"`
	Problem  string   `json:"problem,omitempty"`
}

// View is a saved view of a workspace.
type View struct {
	ID      string         `json:"id"`
	View    string         `json:"view"` // board | calendar | outline | canvas | flow
	Title   string         `json:"title"`
	Kinds   []string       `json:"kinds"`
	Filter  map[string]any `json:"filter,omitempty"`
	GroupBy string         `json:"group_by,omitempty"`
	DateFld string         `json:"date_field,omitempty"`
	Root    string         `json:"root,omitempty"`
	Rel     []string       `json:"rel,omitempty"`
	Layout  map[string]any `json:"layout,omitempty"`
	Rev     int            `json:"rev"`
	Problem string         `json:"problem,omitempty"`
}

// Link is a typed relation from one item to another.
type Link struct {
	Rel string `json:"rel"`
	To  string `json:"to"`
}

// Material is a reference attached to an item.
type Material struct {
	Ref     string `json:"ref"`
	Caption string `json:"caption,omitempty"`
}

// Published records where an item was published.
type Published struct {
	At    string `json:"at"`
	Where string `json:"where"`
	URL   string `json:"url,omitempty"`
	Note  string `json:"note,omitempty"`
}

// Card is an item as a workspace lists it.
type Card struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Stage     string         `json:"stage"`
	Fields    map[string]any `json:"fields"`
	Parent    string         `json:"parent,omitempty"`
	Children  []string       `json:"children"`
	Links     []Link         `json:"links"`
	Material  []Material     `json:"material"`
	Published []Published    `json:"published"`
	Task      string         `json:"task,omitempty"`
	Tags      []string       `json:"tags"`
	Cut       bool           `json:"cut,omitempty"`
	Signed    bool           `json:"signed,omitempty"`
	StaleSign bool           `json:"stale_sign,omitempty"`
	Excerpt   string         `json:"excerpt,omitempty"`
	Words     int            `json:"words"`
	Rev       int            `json:"rev"`
	Updated   string         `json:"updated"`
	Problem   string         `json:"problem,omitempty"`
}

// Detail is one item in full: its card, text, notes, rounds and sign-off.
type Detail struct {
	Card
	Ref      string            `json:"ref"`
	Sections map[string]string `json:"sections"`
	Order    []string          `json:"order"`
	Lead     string            `json:"lead,omitempty"`
	Notes    []Note            `json:"notes"`
	Sending  []Note            `json:"sending"`
	Rounds   []RoundView       `json:"rounds"`
	SignOff  *SignedOff        `json:"sign_off,omitempty"`
	Changed  bool              `json:"changed_since_round"`
	Owners   []string          `json:"owners"`
}

// Space is an open workspace: the workspace, its kinds, items and views.
type Space struct {
	Default   string    `json:"default_view"`
	Workspace Workspace `json:"workspace"`
	Kinds     []Kind    `json:"kinds"`
	Items     []Card    `json:"items"`
	Views     []View    `json:"views"`
}

// Note is a comment or a suggestion on an item. Round 0 is a draft, not yet sent.
type Note struct {
	ID        string `json:"id"`
	OwnerNS   string `json:"owner_ns"`
	WS        string `json:"ws"`
	Item      string `json:"item"`
	ItemRev   int    `json:"item_rev,omitempty"`
	Kind      string `json:"kind"` // comment | suggestion
	Section   string `json:"section,omitempty"`
	Quote     string `json:"quote,omitempty"`
	Replace   string `json:"replace,omitempty"`
	Text      string `json:"text,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Round     int    `json:"round,omitempty"`
	By        string `json:"by,omitempty"`
	BySource  string `json:"by_source,omitempty"` // principal | asserted
	Rev       int    `json:"rev"`
}

// NewNote is a draft note to add: a comment (Text), or a suggestion to replace Quote in
// Section with Replace.
type NewNote struct {
	Kind    string `json:"kind"` // comment | suggestion
	Section string `json:"section,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Replace string `json:"replace,omitempty"`
	Text    string `json:"text,omitempty"`
}

// RoundNote is a note as a sent round holds it.
type RoundNote struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Section string `json:"section,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Replace string `json:"replace,omitempty"`
	Text    string `json:"text,omitempty"`
	State   string `json:"state"` // open | done | declined
	Reply   string `json:"reply,omitempty"`
}

// RoundView is a sent review round.
type RoundView struct {
	Round   int         `json:"round"`
	Ref     string      `json:"ref"`
	SentAt  string      `json:"sent_at"`
	ItemRev int         `json:"item_rev"`
	Digest  string      `json:"digest,omitempty"`
	Notes   []RoundNote `json:"notes"`
	Open    int         `json:"open"`
	Problem string      `json:"problem,omitempty"`
}

// SignedOff is an item's sign-off.
type SignedOff struct {
	At     string `json:"at"`
	Rev    int    `json:"rev"`
	Digest string `json:"digest"`
	Pinned string `json:"pinned,omitempty"`
	Round  int    `json:"round,omitempty"`
}

// Sent is the answer to SendRound.
type Sent struct {
	Round     int      `json:"round"`
	Ref       string   `json:"ref"`
	Notes     int      `json:"notes"`
	Mailed    []string `json:"mailed"`
	NotMailed []string `json:"not_mailed"`
	Commented string   `json:"commented,omitempty"`
}

// Signed is the answer to SignOff.
type Signed struct {
	SignedOff
	Stage     string   `json:"stage,omitempty"`
	Mailed    []string `json:"mailed"`
	NotMailed []string `json:"not_mailed"`
	Problem   string   `json:"problem,omitempty"`
}

// NewWorkspace is a workspace to make. Template, when set, also makes its starting tree.
type NewWorkspace struct {
	Workspace string `json:"workspace"`
	Title     string `json:"title,omitempty"`
	Preset    string `json:"preset,omitempty"`
	About     string `json:"about,omitempty"`
	Template  string `json:"template,omitempty"`
	NS        string `json:"ns,omitempty"`
}

// Created is the answer to NewWorkspace. Problem is set when the workspace was made but its
// template was not all made.
type Created struct {
	Workspace string `json:"workspace"`
	Problem   string `json:"problem,omitempty"`
}

// WorkspaceUpdate changes a workspace; nil fields are left as they are.
type WorkspaceUpdate struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

// Listing is the answer to Workspaces.
type Listing struct {
	Workspaces []Workspace `json:"workspaces"`
	Presets    []Preset    `json:"presets"`
}

// Opened is the answer to Open.
type Opened struct {
	Space     Space    `json:"space"`
	Relations []string `json:"relations"`
}

// NewItem is an item to make.
type NewItem struct {
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Stage    string            `json:"stage"`
	Parent   string            `json:"parent"`
	Fields   map[string]any    `json:"fields"`
	Tags     []string          `json:"tags"`
	Links    []Link            `json:"links"`
	Sections map[string]string `json:"sections"`
}

// Edit changes an item made from revision BaseRev. Nil and empty fields are left as they
// are; a non-nil pointer to a nil slice clears that list. If the item moved on since BaseRev, the service keeps the edit as suggestions and
// answers with a Conflict.
type Edit struct {
	BaseRev   int               `json:"base_rev"`
	Title     *string           `json:"title"`
	Fields    map[string]any    `json:"fields"`
	Sections  map[string]string `json:"sections"`
	Tags      *[]string         `json:"tags"`
	Stage     *string           `json:"stage"`
	Material  *[]Material       `json:"material"`
	Published *[]Published      `json:"published"`
	Links     *[]Link           `json:"links"`
}

// Placement puts an item under Parent, before the sibling Before ("" for last), in View.
type Placement struct {
	Parent string `json:"parent"`
	Before string `json:"before"`
	View   string `json:"view"`
}

// LayoutPatch changes a canvas view's layout; a nil entry removes it.
type LayoutPatch struct {
	Cards  map[string]*Pos        `json:"cards"`
	Stacks map[string]*Stack      `json:"stacks"`
	Notes  map[string]*CanvasNote `json:"notes"`
}

// Pos is a card's place on a canvas.
type Pos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Stack is a group of cards on a canvas.
type Stack struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	X     float64  `json:"x"`
	Y     float64  `json:"y"`
	Items []string `json:"items"`
	Bind  *Bind    `json:"bind,omitempty"`
}

// Bind ties a stack to a stage or a parent.
type Bind struct {
	Key   string `json:"key"` // stage | parent
	Value string `json:"value"`
}

// CanvasNote is a free note on a canvas.
type CanvasNote struct {
	ID   string  `json:"id"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Text string  `json:"text"`
}

// Revision is one revision in an item's history.
type Revision struct {
	Rev   int    `json:"rev"`
	At    string `json:"at"`
	Title string `json:"title"`
}

// Version is an item at one revision: its stored text and, when it could be read, its
// title, stage and sections.
type Version struct {
	Name     string            `json:"name"`
	Rev      int               `json:"rev"`
	Content  string            `json:"content"`
	Title    string            `json:"title,omitempty"`
	Stage    string            `json:"stage,omitempty"`
	Sections map[string]string `json:"sections,omitempty"`
}

// TaskStatus is a Torque task as the service reports it. When the task could not be read
// only ID is set.
type TaskStatus struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// ItemTask is the answer to TorqueStatus: the item's task (nil when it has none) and, when
// the task could not be read, why.
type ItemTask struct {
	Task    *TaskStatus `json:"task"`
	Problem string      `json:"problem,omitempty"`
}

// Event is one change notice. It carries no body: read what it names.
type Event struct {
	ID          string `json:"id"`
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind"`
	OwnerNS     string `json:"owner_ns"`
	Workspace   string `json:"workspace"`
	NS          string `json:"ns"`
	Name        string `json:"name"`
	Note        string `json:"note,omitempty"`
	Rev         int    `json:"rev"`
	Op          string `json:"op"`
	Actor       string `json:"actor"`
	ActorSource string `json:"actor_source"`
	At          string `json:"at"`
}
