// Package sqlitestore persists public WebAuthn state and proof receipts in SQLite.
// The caller opens, secures and closes the database; this package does not read
// files, import credentials or perform any automatic migration of other stores.
package sqlitestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	proof "github.com/hollis-labs/libs/security/webauthn"
)

type Store struct {
	db        *sql.DB
	authority string
}

// New initializes only this package's tables. authority separates trust roots
// sharing a database and must be fixed by trusted application configuration.
func New(ctx context.Context, db *sql.DB, authority string) (*Store, error) {
	if db == nil || authority == "" {
		return nil, errors.New("webauthn: database and authority required")
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS webauthn_subjects (
 authority TEXT NOT NULL, subject TEXT NOT NULL, version INTEGER NOT NULL,
 state BLOB NOT NULL, PRIMARY KEY(authority,subject));
 CREATE TABLE IF NOT EXISTS webauthn_credentials (
 authority TEXT NOT NULL, credential_id BLOB NOT NULL, subject TEXT NOT NULL,
 PRIMARY KEY(authority,credential_id));
 CREATE TABLE IF NOT EXISTS webauthn_challenges (
 authority TEXT NOT NULL, subject TEXT NOT NULL, digest BLOB NOT NULL,
 PRIMARY KEY(authority,digest));`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, authority: authority}, nil
}

func (s *Store) Load(ctx context.Context, subject string) (proof.Snapshot, error) {
	if subject == "" {
		return proof.Snapshot{}, errors.New("webauthn: subject required")
	}
	var raw []byte
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT version,state FROM webauthn_subjects WHERE authority=? AND subject=?`, s.authority, subject).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return proof.Snapshot{}, nil
	}
	if err != nil {
		return proof.Snapshot{}, err
	}
	var out proof.Snapshot
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("webauthn: corrupt stored state: %w", err)
	}
	out.Version = version
	return out, nil
}

func (s *Store) CompareAndSwap(ctx context.Context, subject string, expected int64, next proof.Snapshot) error {
	if subject == "" || expected < 0 || expected == 1<<63-1 || len(next.User.ID) == 0 {
		return errors.New("webauthn: invalid subject state")
	}
	seen := map[string]bool{}
	for _, c := range next.User.Credentials {
		if len(c.ID) == 0 || len(c.PublicKey) == 0 || seen[string(c.ID)] {
			return errors.New("webauthn: invalid or duplicate credential")
		}
		seen[string(c.ID)] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var old proof.Snapshot
	var oldRaw []byte
	var oldVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version,state FROM webauthn_subjects WHERE authority=? AND subject=?`, s.authority, subject).Scan(&oldVersion, &oldRaw)
	if errors.Is(err, sql.ErrNoRows) {
		if expected != 0 {
			return proof.ErrConflict
		}
	} else if err != nil {
		return err
	} else {
		if oldVersion != expected {
			return proof.ErrConflict
		}
		if err = json.Unmarshal(oldRaw, &old); err != nil {
			return err
		}
		if !bytes.Equal(old.User.ID, next.User.ID) {
			return proof.ErrConflict
		}
	}
	// Backfill challenges still present in pre-ledger snapshots before checking
	// new ceremonies. Historical challenges already pruned by older software
	// cannot be recovered; this module has not yet had a production release.
	remember := func(challenge []byte) error {
		digest := sha256.Sum256(challenge)
		_, err := tx.ExecContext(ctx, `INSERT INTO webauthn_challenges(authority,subject,digest) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.authority, subject, digest[:])
		return err
	}
	for _, c := range old.Pending {
		if c.Kind == "assert" || c.Kind == "remove" {
			if err := remember(c.Challenge); err != nil {
				return err
			}
		}
	}
	for _, p := range old.Proofs {
		if err := remember(p.Challenge); err != nil {
			return err
		}
	}
	for id, c := range next.Pending {
		if c.Kind != "assert" && c.Kind != "remove" {
			continue
		}
		if _, exists := old.Pending[id]; exists {
			continue
		}
		digest := sha256.Sum256(c.Challenge)
		result, err := tx.ExecContext(ctx, `INSERT INTO webauthn_challenges(authority,subject,digest) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.authority, subject, digest[:])
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return proof.ErrChallengeReuse
		}
	}

	next.Version = expected + 1
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO webauthn_subjects(authority,subject,version,state) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, s.authority, subject, next.Version, raw)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE webauthn_subjects SET version=?,state=? WHERE authority=? AND subject=? AND version=?`, next.Version, raw, s.authority, subject, expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return proof.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE authority=? AND subject=?`, s.authority, subject); err != nil {
		return err
	}
	for _, c := range next.User.Credentials {
		result, err = tx.ExecContext(ctx, `INSERT INTO webauthn_credentials(authority,credential_id,subject) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.authority, c.ID, subject)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return proof.ErrConflict
		}
	}
	return tx.Commit()
}

var _ proof.Store = (*Store)(nil)
