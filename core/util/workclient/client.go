package workclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Header names the service reads.
const (
	HeaderOwnerNS     = "X-Owner-NS"
	HeaderPrincipal   = "X-Principal-URN"
	HeaderActor       = "X-Actor"
	HeaderCorrelation = "X-Correlation-ID"
)

// maxAnswer bounds a JSON answer.
const maxAnswer = 64 << 20

// Config is where the service is and who calls it.
type Config struct {
	// URL is the service's base, http(s)://host[:port][/base]; the client adds /v1/….
	URL string
	// Token returns the service key, sent as a bearer token. It is called for every
	// request, so a rotated key is picked up. Required; see TokenFile.
	Token func(context.Context) (string, error)
	// OwnerNS is the owner namespace, sent as X-Owner-NS when set. The service uses its
	// own default when it is not.
	OwnerNS string
	// Principal is the signed-in person a delegating service key acts for, sent as
	// X-Principal-URN when set.
	Principal string
	// Actor is an asserted actor, sent as X-Actor when set.
	Actor string
	// CorrelationPrefix starts each X-Correlation-ID; empty means "workclient".
	CorrelationPrefix string
	// HTTP is the client for the JSON routes; nil means one with a 60 s timeout. The event
	// stream uses its Transport without a timeout.
	HTTP *http.Client
}

// TokenFile is a Config.Token that reads the key from path on every request, trimmed of
// surrounding white space. The error never carries the key.
func TokenFile(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("workclient: read the service key file: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", errors.New("workclient: the service key file is empty")
		}
		return tok, nil
	}
}

// Client calls one Work service. It is safe for concurrent use.
type Client struct {
	base   *url.URL
	cfg    Config
	hc     *http.Client
	stream *http.Client
}

// New checks c and makes the client.
func New(c Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.URL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("workclient: the service URL must be an http(s) URL")
	}
	if c.Token == nil {
		return nil, errors.New("workclient: Config.Token is required")
	}
	if c.CorrelationPrefix == "" {
		c.CorrelationPrefix = "workclient"
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		hc = &cp
	}
	if hc.CheckRedirect == nil {
		hc.CheckRedirect = safeRedirect
	}
	cl := &Client{base: u, cfg: c, hc: hc}
	cl.stream = &http.Client{Transport: hc.Transport, CheckRedirect: hc.CheckRedirect, Jar: hc.Jar}
	return cl, nil
}

// safeRedirect follows a redirect only for a read: a redirected write could land on a
// different route than the one asked for.
func safeRedirect(req *http.Request, via []*http.Request) error {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return fmt.Errorf("workclient: refused to follow a redirect for %s", req.Method)
	}
	if len(via) >= 10 {
		return errors.New("workclient: too many redirects")
	}
	return nil
}

// ---- Per-call identity ----

type ctxKey int

const (
	keyOwnerNS ctxKey = iota
	keyPrincipal
	keyActor
	keyCorrelation
)

// none marks a header dropped for one call.
type none struct{}

// WithOwnerNS sets the owner namespace for calls made with ctx. An empty ns leaves
// Config.OwnerNS in place.
func WithOwnerNS(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, keyOwnerNS, ns)
}

// WithPrincipal sets the delegated principal for calls made with ctx. An empty urn leaves
// Config.Principal in place; WithoutPrincipal drops it.
func WithPrincipal(ctx context.Context, urn string) context.Context {
	return context.WithValue(ctx, keyPrincipal, urn)
}

// WithoutPrincipal sends calls made with ctx without X-Principal-URN, whatever Config says.
func WithoutPrincipal(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyPrincipal, none{})
}

// WithActor sets the asserted actor for calls made with ctx. An empty urn leaves
// Config.Actor in place.
func WithActor(ctx context.Context, urn string) context.Context {
	return context.WithValue(ctx, keyActor, urn)
}

// WithCorrelation sends id as the X-Correlation-ID of calls made with ctx, so a caller's
// own request id follows it into the service's log.
func WithCorrelation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyCorrelation, id)
}

func pick(ctx context.Context, k ctxKey, def string) string {
	switch v := ctx.Value(k).(type) {
	case none:
		return ""
	case string:
		if v != "" {
			return v
		}
	}
	return def
}

// OwnerNS is the owner namespace calls made with ctx are sent with ("" leaves it to the
// service's default).
func (c *Client) OwnerNS(ctx context.Context) string {
	return pick(ctx, keyOwnerNS, c.cfg.OwnerNS)
}

// ---- Transport ----

func (c *Client) correlationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return c.cfg.CorrelationPrefix + "-" + hex.EncodeToString(b[:])
}

func (c *Client) newRequest(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Request, error) {
	// path is already escaped (p escapes each part, so a "/" in an id stays inside it); set
	// both forms so the URL is sent exactly as escaped.
	u := *c.base
	raw := strings.TrimRight(u.EscapedPath(), "/") + path
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return nil, err
	}
	u.Path, u.RawPath = dec, raw
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	tok, err := c.cfg.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("workclient: the service key: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	set := func(h, v string) {
		if v != "" {
			req.Header.Set(h, v)
		}
	}
	set(HeaderOwnerNS, pick(ctx, keyOwnerNS, c.cfg.OwnerNS))
	set(HeaderPrincipal, pick(ctx, keyPrincipal, c.cfg.Principal))
	set(HeaderActor, pick(ctx, keyActor, c.cfg.Actor))
	corr := pick(ctx, keyCorrelation, "")
	if corr == "" {
		corr = c.correlationID()
	}
	req.Header.Set(HeaderCorrelation, corr)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// do sends a JSON request and decodes a 2xx answer into out (nil: ignored).
func (c *Client) do(ctx context.Context, method string, path route, q url.Values, in, out any) error {
	if path.err != nil {
		return path.err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path.path, q, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return &wrapped{kind: ErrUnreachable, cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return &wrapped{kind: ErrBadAnswer, cause: err}
	}
	if len(raw) > maxAnswer {
		return &wrapped{kind: ErrBadAnswer, cause: errors.New("the answer is too large")}
	}
	if resp.StatusCode/100 != 2 {
		return answerError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &wrapped{kind: ErrBadAnswer, cause: err}
	}
	return nil
}

// ErrBadID is a workspace, item, note or view id that cannot name one: empty, "." or "..".
// The service's router would clean such a path into another route (a note delete with id
// ".." would become a workspace delete), so the client refuses it before sending.
var ErrBadID = errors.New("workclient: an id may not be empty, \".\" or \"..\"")

// route is an escaped path, or why it could not be made.
type route struct {
	path string
	err  error
}

// fixed is a path with no ids in it.
func fixed(path string) route { return route{path: path} }

// p is /v1/work with each part path-escaped. A part that is empty, "." or ".." is refused.
func p(parts ...string) route {
	var b strings.Builder
	b.WriteString("/v1/work")
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return route{err: ErrBadID}
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return route{path: b.String()}
}

type revOut struct {
	Rev int `json:"rev"`
}

// ---- Workspaces ----

// Workspaces lists the owner namespace's workspaces (of app, when set) and the presets a
// workspace can be made from.
func (c *Client) Workspaces(ctx context.Context, app string) (Listing, error) {
	var out Listing
	q := url.Values{}
	if app != "" {
		q.Set("app", app)
	}
	if err := c.do(ctx, "GET", fixed("/v1/work"), q, nil, &out); err != nil {
		return Listing{}, err
	}
	if out.Workspaces == nil {
		out.Workspaces = []Workspace{}
	}
	return out, nil
}

// NewWorkspace makes a workspace and, when in.Template is set, its starting tree.
func (c *Client) NewWorkspace(ctx context.Context, in NewWorkspace) (Created, error) {
	var out Created
	err := c.do(ctx, "POST", fixed("/v1/work"), nil, in, &out)
	return out, err
}

// Open is a workspace with its kinds, items and views. A workspace that is not there is
// ErrNotFound.
func (c *Client) Open(ctx context.Context, ws string) (Opened, error) {
	var out Opened
	err := c.do(ctx, "GET", p(ws), nil, nil, &out)
	return out, err
}

// UpdateWorkspace renames, archives or unarchives a workspace.
func (c *Client) UpdateWorkspace(ctx context.Context, ws string, u WorkspaceUpdate) error {
	return c.do(ctx, "PUT", p(ws), nil, u, nil)
}

// DeleteWorkspace marks a workspace deleted.
func (c *Client) DeleteWorkspace(ctx context.Context, ws string) error {
	return c.do(ctx, "DELETE", p(ws), nil, nil, nil)
}

// SetOwners sets who owns the workspace: who rounds are mailed to and who may send them,
// sign off and publish. An owner's operation.
func (c *Client) SetOwners(ctx context.Context, ws string, owners []string) error {
	if owners == nil {
		owners = []string{}
	}
	return c.do(ctx, "PUT", p(ws, "owners"), nil, map[string][]string{"owners": owners}, nil)
}

// Log is the revisions of the item stored as name (i.<id>, kind.<kind> or v.<id>).
func (c *Client) Log(ctx context.Context, ws, name string) ([]Revision, error) {
	var out struct {
		Revisions []Revision `json:"revisions"`
	}
	if err := c.do(ctx, "GET", p(ws, "log"), url.Values{"name": {name}}, nil, &out); err != nil {
		return nil, err
	}
	return out.Revisions, nil
}

// At is the item stored as name at revision rev. A revision that is not there is
// ErrNotFound.
func (c *Client) At(ctx context.Context, ws, name string, rev int) (Version, error) {
	var out Version
	err := c.do(ctx, "GET", p(ws, "at"), url.Values{"name": {name}, "rev": {strconv.Itoa(rev)}}, nil, &out)
	return out, err
}

// ---- Items ----

// MakeItem makes an item and returns its id.
func (c *Client) MakeItem(ctx context.Context, ws string, in NewItem) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", p(ws, "items"), nil, in, &out)
	return out.ID, err
}

// Detail is one item in full. An item that is not there is ErrNotFound.
func (c *Client) Detail(ctx context.Context, ws, id string) (Detail, error) {
	var out Detail
	err := c.do(ctx, "GET", p(ws, "items", id), nil, nil, &out)
	return out, err
}

// EditItem edits an item and returns its new revision. See Edit for a stale BaseRev.
func (c *Client) EditItem(ctx context.Context, ws, id string, e Edit) (int, error) {
	e.Tags = clearable(e.Tags)
	e.Material = clearable(e.Material)
	e.Published = clearable(e.Published)
	e.Links = clearable(e.Links)
	var out revOut
	err := c.do(ctx, "PUT", p(ws, "items", id), nil, e, &out)
	return out.Rev, err
}

// clearable makes a pointer to a nil slice a pointer to an empty one: JSON null would
// leave the list as it is, and the caller asked to clear it.
func clearable[T any](v *[]T) *[]T {
	if v != nil && *v == nil {
		return &[]T{}
	}
	return v
}

// Move sets one of an item's keys (stage, or a field) to value and returns its revision.
func (c *Client) Move(ctx context.Context, ws, id, key, value string) (int, error) {
	var out revOut
	err := c.do(ctx, "PUT", p(ws, "items", id, "move"), nil, map[string]string{"key": key, "value": value}, &out)
	return out.Rev, err
}

// Place puts an item under a parent, before a sibling, in a view.
func (c *Client) Place(ctx context.Context, ws, id string, pl Placement) error {
	return c.do(ctx, "PUT", p(ws, "items", id, "place"), nil, pl, nil)
}

// Cut takes an item out of its parent; Restore puts it back.
func (c *Client) Cut(ctx context.Context, ws, id string) error {
	return c.do(ctx, "POST", p(ws, "items", id, "cut"), nil, nil, nil)
}

// Restore puts a cut item back.
func (c *Client) Restore(ctx context.Context, ws, id string) error {
	return c.do(ctx, "POST", p(ws, "items", id, "restore"), nil, nil, nil)
}

// SendToTorque makes the item's Torque task, once. created is false when the item already
// had one. When the task was made but something after failed, the error is an *Error
// whose Task names it, and task is returned too.
func (c *Client) SendToTorque(ctx context.Context, ws, id string) (task string, created bool, err error) {
	var out struct {
		Task    string `json:"task"`
		Created bool   `json:"created"`
	}
	if err := c.do(ctx, "POST", p(ws, "items", id, "torque"), nil, nil, &out); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return e.Task, false, err
		}
		return "", false, err
	}
	return out.Task, out.Created, nil
}

// TorqueStatus is the item's Torque task and its status.
func (c *Client) TorqueStatus(ctx context.Context, ws, id string) (ItemTask, error) {
	var out ItemTask
	err := c.do(ctx, "GET", p(ws, "items", id, "torque"), nil, nil, &out)
	return out, err
}

// ---- Notes, rounds, sign-off ----

// AddNote adds a draft comment or suggestion to an item and returns it as stored.
func (c *Client) AddNote(ctx context.Context, ws, id string, n NewNote) (Note, error) {
	var out Note
	err := c.do(ctx, "POST", p(ws, "items", id, "notes"), nil, n, &out)
	return out, err
}

// EditNote changes a draft note's text or replacement; nil leaves it as it is.
func (c *Client) EditNote(ctx context.Context, ws, noteID string, text, replace *string) error {
	in := map[string]*string{}
	if text != nil {
		in["text"] = text
	}
	if replace != nil {
		in["replace"] = replace
	}
	return c.do(ctx, "PUT", p(ws, "notes", noteID), nil, in, nil)
}

// DropNote removes a draft note.
func (c *Client) DropNote(ctx context.Context, ws, noteID string) error {
	return c.do(ctx, "DELETE", p(ws, "notes", noteID), nil, nil, nil)
}

// SendRound sends the item's draft notes as a review round. An owner's operation.
func (c *Client) SendRound(ctx context.Context, ws, id string) (Sent, error) {
	var out Sent
	err := c.do(ctx, "POST", p(ws, "items", id, "round"), nil, nil, &out)
	return out, err
}

// SignOff signs the item off; anyway signs off over open notes. Refused over open notes
// with an *Error carrying OpenNotes. An owner's operation.
func (c *Client) SignOff(ctx context.Context, ws, id string, anyway bool) (Signed, error) {
	var out Signed
	err := c.do(ctx, "POST", p(ws, "items", id, "signoff"), nil, map[string]bool{"anyway": anyway}, &out)
	return out, err
}

// Unsign takes the item's sign-off back. An owner's operation.
func (c *Client) Unsign(ctx context.Context, ws, id string) error {
	return c.do(ctx, "DELETE", p(ws, "items", id, "signoff"), nil, nil, nil)
}

// ---- Views ----

// NewView makes a view from set and returns the id the service gave it.
func (c *Client) NewView(ctx context.Context, ws string, set map[string]any) (id string, rev int, err error) {
	var out struct {
		ID  string `json:"id"`
		Rev int    `json:"rev"`
	}
	if set == nil {
		set = map[string]any{}
	}
	err = c.do(ctx, "POST", p(ws, "views"), nil, set, &out)
	return out.ID, out.Rev, err
}

// SaveView changes the view id, or makes it with that id when it is not there yet.
func (c *Client) SaveView(ctx context.Context, ws, id string, set map[string]any) (int, error) {
	var out revOut
	if set == nil {
		set = map[string]any{}
	}
	err := c.do(ctx, "PUT", p(ws, "views", id), nil, set, &out)
	return out.Rev, err
}

// PatchLayout changes a canvas view's layout.
func (c *Client) PatchLayout(ctx context.Context, ws, view string, lp LayoutPatch) (int, error) {
	var out revOut
	err := c.do(ctx, "PUT", p(ws, "views", view, "layout"), nil, lp, &out)
	return out.Rev, err
}
