package toolresult

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// Defaults applied by [New] to zero [Config] fields.
const (
	DefaultHardCapBytes = 1024 * 1024
	DefaultTTL          = time.Hour
	DefaultBudget       = 2 * 1024
	DefaultScheme       = "tool_result"
	DefaultFetchName    = "fetch_tool_result"
	DefaultSearchName   = "search_tool_result"
)

// partialPreviewHeader opens every preview [Cache.Present] returns.
const partialPreviewHeader = "[PARTIAL PREVIEW — not a complete read. Omitted content must be retrieved before making claims about it.]\n"

// Config configures a [Cache]. Every field is optional.
type Config struct {
	// HardCapBytes is the largest body stored (default 1 MiB). A larger body
	// is cached as metadata only and cannot be read back. It also caps every
	// budget and page.
	HardCapBytes int
	// TTL is how long an entry lives (default 1 hour). Purge policy is the
	// host's decision; see [Cache.Purge].
	TTL time.Duration
	// DefaultBudget is the preview and page budget used when a caller passes
	// a budget of zero or less (default 2048).
	DefaultBudget int
	// NewID returns a fresh pointer id (default: a ULID).
	NewID func() string
	// Now is the clock (default [time.Now]); tests inject a fake.
	Now func() time.Time
	// Scheme is the pointer scheme printed in the footer (default
	// "tool_result", giving tool_result://<id>).
	Scheme string
	// FetchName and SearchName are the agent-facing tool names the footer
	// and the specs refer to (defaults "fetch_tool_result" and
	// "search_tool_result").
	FetchName, SearchName string
	// Footer renders the recovery notice appended to a preview. The default
	// is the Nanite text. Returning "" omits the notice.
	Footer func(FooterData) string
	// Exempt reports tools whose results are never cached or previewed, for
	// example discovery tools whose output is what the agent reads to decide
	// its next step.
	Exempt func(tool string) bool
}

// FooterData is the input to [Config.Footer].
type FooterData struct {
	// ID is the pointer id; Scheme, FetchName and SearchName are the
	// configured values.
	ID, Scheme, FetchName, SearchName string
	// Tool is the producing tool.
	Tool string
	// ByteSize is the original body size.
	ByteSize int
	// ExpiresAt is when the entry expires.
	ExpiresAt time.Time
	// Stored is false when the body exceeded HardCapBytes and only metadata
	// was cached.
	Stored bool
	// HardCapBytes is the configured storage cap.
	HardCapBytes int
}

// Meta describes the result being presented.
type Meta struct {
	// Tool is the producing tool's name.
	Tool string
	// CallID is the model's tool-call id.
	CallID string
	// IsError marks an error result. Errors are short and load-bearing for
	// the agent's recovery, so they are never cached or previewed.
	IsError bool
}

// Pointer identifies a stored result.
type Pointer struct {
	// ID is the id to fetch or search by.
	ID string
	// ExpiresAt is when the entry expires.
	ExpiresAt time.Time
	// ByteSize is the size of the original body.
	ByteSize int
	// Stored is false when only metadata was cached (body over the hard
	// cap).
	Stored bool
}

// View is what [Cache.Present] shows the model.
type View struct {
	// Content is the complete LLM-visible text: the preview header, the
	// preview and the Footer. When nothing was cached it is the original
	// body.
	Content string
	// Footer is the recovery notice alone, for hosts that place it
	// elsewhere (for example in a list envelope's hint). Empty when nothing
	// was cached.
	Footer string
	// CacheID is Pointer.ID; empty when nothing was cached.
	CacheID string
	// Format is "complete" (nothing cached), "json" or "text".
	Format string
	// OriginalBytes is the size of the original body.
	OriginalBytes int
	// BudgetBytes is the preview budget actually applied.
	BudgetBytes int
	// Cached reports that the body was stored and Content is a preview.
	Cached bool
	// Pointer is set when Cached.
	Pointer Pointer
}

// Cache turns oversized tool results into a bounded preview plus a pointer,
// stores the original under a host-derived scope, and serves it back by id.
// It is safe for concurrent use when its [Store] is.
type Cache struct {
	store Store
	cfg   Config
}

// New returns a Cache over s with c's zero fields defaulted.
func New(s Store, c Config) *Cache {
	if c.HardCapBytes <= 0 {
		c.HardCapBytes = DefaultHardCapBytes
	}
	if c.TTL <= 0 {
		c.TTL = DefaultTTL
	}
	if c.DefaultBudget <= 0 {
		c.DefaultBudget = DefaultBudget
	}
	c.DefaultBudget = min(c.DefaultBudget, c.HardCapBytes)
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.NewID == nil {
		now := c.Now
		c.NewID = func() string { return ulid.MustNew(ulid.Timestamp(now()), rand.Reader).String() }
	}
	if c.Scheme == "" {
		c.Scheme = DefaultScheme
	}
	if c.FetchName == "" {
		c.FetchName = DefaultFetchName
	}
	if c.SearchName == "" {
		c.SearchName = DefaultSearchName
	}
	if c.Footer == nil {
		c.Footer = DefaultFooter
	}
	return &Cache{store: s, cfg: c}
}

// DefaultFooter is the Nanite recovery notice, used when [Config.Footer] is
// nil.
func DefaultFooter(d FooterData) string {
	if !d.Stored {
		return fmt.Sprintf("[TRUNCATED — result exceeded the %d-byte storage cap. Only metadata was cached as %s://%s; full content is unavailable. Narrow the source query.]", d.HardCapBytes, d.Scheme, d.ID)
	}
	return fmt.Sprintf("[TRUNCATED — full result cached as %s://%s (total_size=%d bytes, expires_at=%s). "+
		"Use %s({\"id\":\"%s\"}) for pages, optionally with json_pointer to select a field (for example /stdout). "+
		"Use %s({\"id\":\"%s\",\"pattern\":\"...\"}) for matching regions. Preview labels are JSON pointers; fetch offsets address the selected text.]",
		d.Scheme, d.ID, d.ByteSize, d.ExpiresAt.UTC().Format(time.RFC3339), d.FetchName, d.ID, d.SearchName, d.ID)
}

// Put stores body under scope without building a preview, for hosts that
// compose their own message (for example a list envelope whose hint gets a
// pointer sentence). Put ignores Exempt and IsError; a body over HardCapBytes
// is stored as metadata only.
func (c *Cache) Put(ctx context.Context, scope string, m Meta, body string) (Pointer, error) {
	if scope == "" {
		return Pointer{}, ErrEmptyScope
	}
	now := c.cfg.Now().UTC()
	e := Entry{
		ID: c.cfg.NewID(), Scope: scope, Tool: m.Tool, CallID: m.CallID,
		CreatedAt: now, ExpiresAt: now.Add(c.cfg.TTL), ByteSize: len(body),
	}
	if len(body) <= c.cfg.HardCapBytes {
		e.Body, e.BodyStored = body, true
	}
	if err := c.store.Put(ctx, e); err != nil {
		return Pointer{}, fmt.Errorf("cache store: %w", err)
	}
	return Pointer{ID: e.ID, ExpiresAt: e.ExpiresAt, ByteSize: e.ByteSize, Stored: e.BodyStored}, nil
}

// Present returns the LLM-visible form of body. A result that fits budget, an
// error result, or a result of an exempt tool is returned unchanged and
// uncached. Otherwise the original is stored under scope and the view is a
// bounded [Preview] plus a recovery footer; budget bounds the preview content
// and the notice is additional. A budget of zero or less uses
// [Config.DefaultBudget], and no budget exceeds [Config.HardCapBytes].
//
// Present stores before it previews. If the store fails, or scope is empty
// when storage is needed, it returns the unmodified body in View.Content with
// the error, and the footer never mentions an id that was not stored; the
// host should then fall back to its own truncation.
func (c *Cache) Present(ctx context.Context, scope string, m Meta, body string, budget int) (View, error) {
	if budget <= 0 {
		budget = c.cfg.DefaultBudget
	}
	budget = min(budget, c.cfg.HardCapBytes)
	view := View{Content: body, Format: "complete", OriginalBytes: len(body), BudgetBytes: budget}
	if m.IsError || (c.cfg.Exempt != nil && c.cfg.Exempt(m.Tool)) || len(body) <= budget {
		return view, nil
	}
	ptr, err := c.Put(ctx, scope, m, body)
	if err != nil {
		return view, err
	}
	preview, format := Preview(body, budget)
	footer := c.cfg.Footer(FooterData{
		ID: ptr.ID, Scheme: c.cfg.Scheme, FetchName: c.cfg.FetchName, SearchName: c.cfg.SearchName,
		Tool: m.Tool, ByteSize: ptr.ByteSize, ExpiresAt: ptr.ExpiresAt, Stored: ptr.Stored, HardCapBytes: c.cfg.HardCapBytes,
	})
	view.CacheID, view.Cached, view.Format, view.Pointer, view.Footer = ptr.ID, true, format, ptr, footer
	view.Content = partialPreviewHeader + preview
	if footer != "" {
		view.Content += "\n\n" + footer
	}
	return view, nil
}

// entry loads a live entry: present in scope, unexpired, body stored.
func (c *Cache) entry(ctx context.Context, scope, id string) (Entry, error) {
	if scope == "" {
		return Entry{}, ErrEmptyScope
	}
	e, err := c.store.Get(ctx, scope, id)
	if errors.Is(err, ErrNotFound) {
		return Entry{}, &resultError{ErrNotFound, fmt.Sprintf("cached result %q not found or expired", id)}
	}
	if err != nil {
		return Entry{}, fmt.Errorf("cache fetch: %w", err)
	}
	if c.cfg.Now().After(e.ExpiresAt) {
		return Entry{}, &resultError{ErrExpired, fmt.Sprintf("cached result %q has expired", id)}
	}
	if !e.BodyStored {
		return Entry{}, &resultError{ErrBodyNotStored, fmt.Sprintf("cached result %q exceeded hard cap (%d bytes); body not stored", id, e.ByteSize)}
	}
	return e, nil
}

// selected loads the entry and applies the JSON pointer.
func (c *Cache) selected(ctx context.Context, scope, id, pointer string) (string, error) {
	e, err := c.entry(ctx, scope, id)
	if err != nil {
		return "", err
	}
	return Select(e.Body, pointer)
}

func (c *Cache) clamp(budget int) int {
	if budget <= 0 {
		budget = c.cfg.DefaultBudget
	}
	return min(budget, c.cfg.HardCapBytes)
}

// Read returns one page of the stored result. A non-empty jsonPointer selects
// a value inside a JSON result first (see [Select]); offset and length address
// UTF-8 bytes of the selected value. length defaults to, and is capped by,
// budget. It fails with [ErrNotFound] for an unknown id or another scope's id,
// [ErrExpired], [ErrBodyNotStored] and [ErrEmptyScope].
func (c *Cache) Read(ctx context.Context, scope, id, jsonPointer string, offset, length, budget int) (Page, error) {
	body, err := c.selected(ctx, scope, id, jsonPointer)
	if err != nil {
		return Page{}, err
	}
	page, err := ReadPage(body, offset, length, c.clamp(budget))
	page.JSONPointer = jsonPointer
	return page, err
}

// Search returns matching lines of the stored result; see [SearchPage] and
// [Cache.Read] for pointer, offset and error semantics.
func (c *Cache) Search(ctx context.Context, scope, id, jsonPointer, pattern string, offset, maxMatches, budget int) (SearchResult, error) {
	body, err := c.selected(ctx, scope, id, jsonPointer)
	if err != nil {
		return SearchResult{}, err
	}
	res, err := SearchPage(body, pattern, offset, maxMatches, c.clamp(budget))
	res.JSONPointer = jsonPointer
	return res, err
}

// Purge deletes expired entries and returns how many were removed. Nothing
// calls it automatically; when and whether to purge is the host's policy.
func (c *Cache) Purge(ctx context.Context) (int, error) {
	n, err := c.store.DeleteExpired(ctx, c.cfg.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("cache purge: %w", err)
	}
	return n, nil
}

// resultError carries the agent-readable message and wraps a sentinel.
type resultError struct {
	sentinel error
	msg      string
}

func (e *resultError) Error() string { return e.msg }
func (e *resultError) Unwrap() error { return e.sentinel }
