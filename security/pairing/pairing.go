// Package pairing defines version 1 of device enrollment and a SQLite-backed
// standalone implementation. Authentication, HTTP admission, and app-specific
// grants remain the host's responsibility. Device credentials are not mesh JWTs.
package pairing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/hollis-labs/libs/security/auth"
)

const Version = "1"
const ProtocolHeader = "Hollis-Pairing-Protocol"
const ExchangePath = "/auth/pair/exchange"
const RenewPath = "/auth/renew"

var ErrGrant = errors.New("pairing: invalid or inactive grant")
var ErrInput = errors.New("pairing: invalid input")

type Grant struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Audience  string    `json:"audience"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
}
type ExchangeRequest struct {
	Code     string   `json:"code"`
	Audience string   `json:"audience"`
	Scopes   []string `json:"scopes,omitempty"`
}
type Device struct {
	ID        string     `json:"id"`
	OwnerID   string     `json:"owner_id"`
	Label     string     `json:"label"`
	Audience  string     `json:"audience"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
type Exchange struct {
	Device    Device `json:"device"`
	Token     string `json:"token"`
	TokenType string `json:"token_type"`
}
type Config struct {
	Audience  string
	Scopes    []string
	GrantTTL  time.Duration // defaults to five minutes; at most one hour
	DeviceTTL time.Duration // defaults to thirty days; at most thirty days
	Now       func() time.Time
}
type Store struct {
	db  *sql.DB
	cfg Config
}

func secret(prefix string) string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:])
}

// Origin accepts HTTPS, or explicit HTTP loopback for an authenticated tunnel.
// It excludes credentials, paths, query parameters and fragments. Clients must
// reject redirects and bind secret storage to this exact normalized origin.
func Origin(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", ErrInput
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", ErrInput
	}
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return "", ErrInput
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}

func textOK(s string, max int) bool {
	return s != "" && len(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}
func normalize(scopes, allowed []string) ([]string, error) {
	if len(scopes) == 0 || len(scopes) > 32 {
		return nil, ErrInput
	}
	result := slices.Clone(scopes)
	for _, s := range result {
		if !textOK(s, 128) || strings.ContainsAny(s, " *\t\r\n") || (allowed != nil && !slices.Contains(allowed, s)) {
			return nil, ErrInput
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// Open installs namespaced tables in an application-owned SQLite database. The
// caller configures its SQLite driver, busy timeout, and transaction concurrency.
func Open(db *sql.DB, cfg Config) (*Store, error) {
	if !textOK(cfg.Audience, 128) {
		return nil, ErrInput
	}
	scopes, e := normalize(cfg.Scopes, nil)
	if e != nil {
		return nil, e
	}
	cfg.Scopes = scopes
	if cfg.GrantTTL == 0 {
		cfg.GrantTTL = 5 * time.Minute
	}
	if cfg.DeviceTTL == 0 {
		cfg.DeviceTTL = 30 * 24 * time.Hour
	}
	if cfg.GrantTTL < time.Second || cfg.GrantTTL > time.Hour || cfg.DeviceTTL < time.Second || cfg.DeviceTTL > 30*24*time.Hour {
		return nil, ErrInput
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS hollis_pairing_grants(
 id TEXT PRIMARY KEY, code_hash TEXT NOT NULL UNIQUE, audience TEXT NOT NULL, owner_id TEXT NOT NULL,
 label TEXT NOT NULL, scopes TEXT NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER, revoked_at INTEGER);
CREATE TABLE IF NOT EXISTS hollis_pairing_devices(
 id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, audience TEXT NOT NULL, owner_id TEXT NOT NULL,
 label TEXT NOT NULL, scopes TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS hollis_pairing_owner ON hollis_pairing_devices(audience,owner_id);`)
	if e != nil {
		return nil, e
	}
	return &Store{db: db, cfg: cfg}, nil
}

// CreateGrant must be called only after the host authenticates an administrator
// of ownerID. Neither an ordinary device nor unverified user input grants this authority.
func (s *Store) CreateGrant(ctx context.Context, ownerID, label string, scopes []string) (Grant, error) {
	if !textOK(ownerID, 200) || !textOK(label, 128) {
		return Grant{}, ErrInput
	}
	scopes, e := normalize(scopes, s.cfg.Scopes)
	if e != nil {
		return Grant{}, e
	}
	now := s.cfg.Now().UTC().Truncate(time.Second)
	g := Grant{ID: secret("grt_"), Code: secret("hpg_"), Audience: s.cfg.Audience, Scopes: scopes, ExpiresAt: now.Add(s.cfg.GrantTTL).Truncate(time.Second)}
	raw, _ := json.Marshal(scopes)
	_, e = s.db.ExecContext(ctx, "INSERT INTO hollis_pairing_grants(id,code_hash,audience,owner_id,label,scopes,expires_at) VALUES(?,?,?,?,?,?,?)", g.ID, auth.HashKey(g.Code), g.Audience, ownerID, label, string(raw), g.ExpiresAt.Unix())
	if e != nil {
		return Grant{}, e
	}
	return g, nil
}

// Exchange consumes the grant and persists the device in one transaction.
// Failed, expired, revoked, replayed, wrong-audience, and widening requests have
// the same refusal. A lost successful response requires a fresh grant; plaintext
// device credentials are deliberately not retained for response replay.
func (s *Store) Exchange(ctx context.Context, in ExchangeRequest) (Exchange, error) {
	if len(in.Code) > 256 || in.Audience != s.cfg.Audience {
		return Exchange{}, ErrGrant
	}
	var requested any
	if in.Scopes != nil {
		scopes, e := normalize(in.Scopes, s.cfg.Scopes)
		if e != nil {
			return Exchange{}, ErrGrant
		}
		raw, _ := json.Marshal(scopes)
		requested = string(raw)
		in.Scopes = scopes
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Exchange{}, e
	}
	defer tx.Rollback()
	now := s.cfg.Now().UTC().Truncate(time.Second)
	var owner, label, raw string
	e = tx.QueryRowContext(ctx, `UPDATE hollis_pairing_grants SET consumed_at=?
 WHERE code_hash=? AND audience=? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?
 AND (? IS NULL OR NOT EXISTS (SELECT 1 FROM json_each(?) requested WHERE requested.value NOT IN(SELECT value FROM json_each(hollis_pairing_grants.scopes))))
 RETURNING owner_id,label,scopes`, now.Unix(), auth.HashKey(in.Code), s.cfg.Audience, now.Unix(), requested, requested).Scan(&owner, &label, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return Exchange{}, ErrGrant
	}
	if e != nil {
		return Exchange{}, e
	}
	var scopes []string
	if e = json.Unmarshal([]byte(raw), &scopes); e != nil {
		return Exchange{}, e
	}
	if in.Scopes != nil {
		scopes = in.Scopes
	}
	if _, err := normalize(scopes, s.cfg.Scopes); err != nil {
		return Exchange{}, ErrGrant
	}
	d := Device{ID: secret("dev_"), OwnerID: owner, Label: label, Audience: s.cfg.Audience, Scopes: scopes, CreatedAt: now, ExpiresAt: now.Add(s.cfg.DeviceTTL).Truncate(time.Second)}
	token := secret("hpd_")
	encoded, _ := json.Marshal(scopes)
	_, e = tx.ExecContext(ctx, `INSERT INTO hollis_pairing_devices(id,token_hash,audience,owner_id,label,scopes,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, d.ID, auth.HashKey(token), d.Audience, owner, label, string(encoded), now.Unix(), d.ExpiresAt.Unix())
	if e != nil {
		return Exchange{}, e
	}
	if e = tx.Commit(); e != nil {
		return Exchange{}, e
	}
	return Exchange{Device: d, Token: token, TokenType: "Bearer"}, nil
}

type scanner interface{ Scan(...any) error }

const deviceColumns = "id,owner_id,label,audience,scopes,created_at,expires_at,revoked_at"

func readDevice(row scanner) (Device, error) {
	var d Device
	var raw string
	var created, expires int64
	var revoked sql.NullInt64
	e := row.Scan(&d.ID, &d.OwnerID, &d.Label, &d.Audience, &raw, &created, &expires, &revoked)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal([]byte(raw), &d.Scopes)
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.ExpiresAt = time.Unix(expires, 0).UTC()
	if revoked.Valid {
		at := time.Unix(revoked.Int64, 0).UTC()
		d.RevokedAt = &at
	}
	return d, e
}
func (s *Store) Device(ctx context.Context, token string) (Device, error) {
	return s.device(ctx, s.db, token)
}

// DeviceInTransaction resolves a credential within a caller's transaction on
// the same database, so authenticated application writes and revocation serialize.
func (s *Store) DeviceInTransaction(ctx context.Context, tx *sql.Tx, token string) (Device, error) {
	return s.device(ctx, tx, token)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) device(ctx context.Context, q queryer, token string) (Device, error) {
	if len(token) > 256 || !strings.HasPrefix(token, "hpd_") {
		return Device{}, auth.ErrInvalidToken
	}
	d, e := readDevice(q.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM hollis_pairing_devices WHERE token_hash=? AND audience=? AND revoked_at IS NULL AND expires_at>?", auth.HashKey(token), s.cfg.Audience, s.cfg.Now().Unix()))
	if errors.Is(e, sql.ErrNoRows) {
		return Device{}, auth.ErrInvalidToken
	}
	return d, e
}

// Verify implements auth.Verifier; a fresh database read observes revocation.
func (s *Store) Verify(ctx context.Context, token string) (auth.Principal, error) {
	d, e := s.Device(ctx, token)
	if e != nil {
		return auth.Principal{}, e
	}
	return auth.Principal{Verified: true, ID: d.ID, Kind: auth.KindDevice, Display: d.Label, Audience: d.Audience, Scopes: d.Scopes, ExpiresAt: d.ExpiresAt}, nil
}
func (s *Store) List(ctx context.Context, ownerID string) ([]Device, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+deviceColumns+" FROM hollis_pairing_devices WHERE audience=? AND owner_id=? ORDER BY created_at DESC,id LIMIT 200", s.cfg.Audience, ownerID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, e := readDevice(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) Revoke(ctx context.Context, ownerID, id string) error {
	r, e := s.db.ExecContext(ctx, "UPDATE hollis_pairing_devices SET revoked_at=COALESCE(revoked_at,?) WHERE id=? AND audience=? AND owner_id=?", s.cfg.Now().Unix(), id, s.cfg.Audience, ownerID)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return auth.ErrInvalidToken
	}
	return nil
}
func (s *Store) RevokeGrant(ctx context.Context, ownerID, id string) error {
	_, e := s.db.ExecContext(ctx, "UPDATE hollis_pairing_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=? AND audience=? AND owner_id=?", s.cfg.Now().Unix(), id, s.cfg.Audience, ownerID)
	return e
}

// Renew keeps the credential but extends its lifetime. Rechecking the current
// row and requested scope subset atomically prevents restoring revoked rights.
func (s *Store) Renew(ctx context.Context, token string, scopes []string) (Device, error) {
	var requested any
	if scopes != nil {
		n, e := normalize(scopes, s.cfg.Scopes)
		if e != nil {
			return Device{}, ErrInput
		}
		b, _ := json.Marshal(n)
		requested = string(b)
	}
	now := s.cfg.Now().Unix()
	d, e := readDevice(s.db.QueryRowContext(ctx, `UPDATE hollis_pairing_devices SET expires_at=?,scopes=COALESCE(?,scopes)
 WHERE token_hash=? AND audience=? AND revoked_at IS NULL AND expires_at>?
 AND (? IS NULL OR NOT EXISTS(SELECT 1 FROM json_each(?) requested WHERE requested.value NOT IN(SELECT value FROM json_each(hollis_pairing_devices.scopes)))) RETURNING `+deviceColumns,
		now+int64(s.cfg.DeviceTTL.Seconds()), requested, auth.HashKey(token), s.cfg.Audience, now, requested, requested))
	if errors.Is(e, sql.ErrNoRows) {
		return Device{}, auth.ErrInvalidToken
	}
	return d, e
}
