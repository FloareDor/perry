// Package sqlite implements a durable perry store on a single SQLite file.
//
// It is the default backend: one file per repository, no server to run, and
// transactional reference updates. The object table is append-only, which is
// what lets concurrent pushes write objects without coordinating.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, so no cgo and no C toolchain

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/store"
)

// Store is a SQLite-backed object and reference store.
type Store struct {
	db *sql.DB
}

var _ store.Store = (*Store)(nil)

const schema = `
CREATE TABLE IF NOT EXISTS objects (
    id      BLOB PRIMARY KEY,  -- the 20-byte SHA-1, not its hex form
    type    INTEGER NOT NULL,
    payload BLOB NOT NULL
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS refs (
    name   TEXT PRIMARY KEY,
    target BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// Open opens or creates a repository database at path.
//
// Use ":memory:" for an ephemeral store.
func Open(path string) (*Store, error) {
	// WAL lets readers run while a write is in flight, which is what keeps
	// a fetch from blocking behind a concurrent push. busy_timeout covers
	// the brief moments when two writers do collide, since SQLite allows
	// only one writer at a time.
	// _txlock=immediate makes every transaction take the write lock at BEGIN
	// instead of at its first write. Reference updates read an expected value
	// and then write based on it, and with a deferred lock two concurrent
	// updates could both read the old value before either wrote. SQLite would
	// then hand the loser SQLITE_BUSY after it had already done its work, and
	// a retry would be a second full round trip.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle, for the read API's own queries.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Get(ctx context.Context, id object.ID) (*object.Object, error) {
	var typ int
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT type, payload FROM objects WHERE id = ?`, id[:]).Scan(&typ, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: object %s", store.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get %s: %w", id, err)
	}
	return &object.Object{Type: object.Type(typ), Payload: payload}, nil
}

func (s *Store) Has(ctx context.Context, id object.ID) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM objects WHERE id = ?`, id[:]).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sqlite: has %s: %w", id, err)
	}
	return true, nil
}

// payloadFor returns a payload that is never nil.
//
// database/sql maps a nil []byte to SQL NULL, and the objects table forbids
// it. Git has legitimate zero-length objects, an empty file and the empty
// tree among them, so a nil payload has to reach SQLite as an empty blob
// rather than as NULL.
func payloadFor(obj *object.Object) []byte {
	if obj.Payload == nil {
		return []byte{}
	}
	return obj.Payload
}

func (s *Store) Put(ctx context.Context, obj *object.Object) (object.ID, error) {
	id := obj.ID()
	// An object's name is its content, so a second write of the same name
	// is the same bytes. Ignoring the conflict is correct and saves a read.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO objects (id, type, payload) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		id[:], int(obj.Type), payloadFor(obj))
	if err != nil {
		return id, fmt.Errorf("sqlite: put %s: %w", id, err)
	}
	return id, nil
}

// PutBatch writes many objects in one transaction.
//
// A push of a few thousand objects as individual statements would mean a few
// thousand fsyncs. One transaction and one prepared statement turns that
// into a single commit.
func (s *Store) PutBatch(ctx context.Context, objs []*object.Object) error {
	if len(objs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO objects (id, type, payload) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare: %w", err)
	}
	defer stmt.Close()

	for _, o := range objs {
		id := o.ID()
		if _, err := stmt.ExecContext(ctx, id[:], int(o.Type), payloadFor(o)); err != nil {
			return fmt.Errorf("sqlite: put %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count: %w", err)
	}
	return n, nil
}

func (s *Store) GetRef(ctx context.Context, name string) (store.Ref, error) {
	var target []byte
	err := s.db.QueryRowContext(ctx, `SELECT target FROM refs WHERE name = ?`, name).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Ref{}, fmt.Errorf("%w: ref %s", store.ErrNotFound, name)
	}
	if err != nil {
		return store.Ref{}, fmt.Errorf("sqlite: get ref %s: %w", name, err)
	}
	id, err := object.IDFromBytes(target)
	if err != nil {
		return store.Ref{}, fmt.Errorf("sqlite: ref %s holds a bad id: %w", name, err)
	}
	return store.Ref{Name: name, Target: id}, nil
}

func (s *Store) ListRefs(ctx context.Context, prefix string) ([]store.Ref, error) {
	// LIKE with an escaped prefix, since a ref name may legitimately
	// contain characters LIKE treats as wildcards.
	pattern := escapeLike(prefix) + "%"
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, target FROM refs WHERE name LIKE ? ESCAPE '\' ORDER BY name`, pattern)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list refs: %w", err)
	}
	defer rows.Close()

	var out []store.Ref
	for rows.Next() {
		var name string
		var target []byte
		if err := rows.Scan(&name, &target); err != nil {
			return nil, err
		}
		id, err := object.IDFromBytes(target)
		if err != nil {
			return nil, fmt.Errorf("sqlite: ref %s holds a bad id: %w", name, err)
		}
		out = append(out, store.Ref{Name: name, Target: id})
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// UpdateRefs applies a batch of compare-and-swap updates atomically.
//
// This is the only place a repository serializes. SQLite allows one writer
// at a time, so the transaction is the compare-and-swap: every expected
// value is checked inside it, and a mismatch rolls the whole batch back.
func (s *Store) UpdateRefs(ctx context.Context, updates []store.RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	// The connection is opened with _txlock=immediate, so this BEGIN takes
	// the write lock before reading any expected value. That is what makes
	// the check-then-write below a real compare-and-swap.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer tx.Rollback()

	for _, u := range updates {
		var actual object.ID
		var target []byte
		err := tx.QueryRowContext(ctx, `SELECT target FROM refs WHERE name = ?`, u.Name).Scan(&target)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// actual stays zero, which is what "must not exist" compares to
		case err != nil:
			return fmt.Errorf("sqlite: read ref %s: %w", u.Name, err)
		default:
			if actual, err = object.IDFromBytes(target); err != nil {
				return fmt.Errorf("sqlite: ref %s holds a bad id: %w", u.Name, err)
			}
		}
		if actual != u.Old {
			return &store.ErrRefConflict{Ref: u.Name, Expected: u.Old, Actual: actual}
		}
	}

	for _, u := range updates {
		if u.New.IsZero() {
			if _, err := tx.ExecContext(ctx, `DELETE FROM refs WHERE name = ?`, u.Name); err != nil {
				return fmt.Errorf("sqlite: delete ref %s: %w", u.Name, err)
			}
			continue
		}
		newTarget := u.New
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO refs (name, target) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET target = excluded.target`,
			u.Name, newTarget[:]); err != nil {
			return fmt.Errorf("sqlite: set ref %s: %w", u.Name, err)
		}
	}
	return tx.Commit()
}

const headKey = "head"

func (s *Store) HeadTarget(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, headKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "refs/heads/main", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: head: %w", err)
	}
	return v, nil
}

func (s *Store) SetHeadTarget(ctx context.Context, target string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		headKey, target)
	if err != nil {
		return fmt.Errorf("sqlite: set head: %w", err)
	}
	return nil
}
