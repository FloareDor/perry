// Package store defines the object and reference storage a repository runs
// on, plus the in-memory implementation used by tests.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/FloareDor/perry/internal/object"
)

// ErrNotFound reports a missing object or reference.
var ErrNotFound = errors.New("store: not found")

// ErrRefConflict reports a compare-and-swap that lost.
//
// It carries the value the reference actually held, so a caller that wants
// to retry can rebase onto it without a second round trip.
type ErrRefConflict struct {
	Ref      string
	Expected object.ID
	Actual   object.ID
}

func (e *ErrRefConflict) Error() string {
	return fmt.Sprintf("store: %s expected %s but holds %s", e.Ref, short(e.Expected), short(e.Actual))
}

func short(id object.ID) string {
	if id.IsZero() {
		return "<nothing>"
	}
	return id.Short(8)
}

// Ref is a named pointer into the object graph.
type Ref struct {
	Name   string    // full name, such as refs/heads/main
	Target object.ID // the commit or tag it points at
}

// IsBranch reports whether the ref lives under refs/heads/.
func (r Ref) IsBranch() bool { return strings.HasPrefix(r.Name, "refs/heads/") }

// IsTag reports whether the ref lives under refs/tags/.
func (r Ref) IsTag() bool { return strings.HasPrefix(r.Name, "refs/tags/") }

// ShortName strips the refs/heads/ or refs/tags/ prefix.
func (r Ref) ShortName() string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/"} {
		if strings.HasPrefix(r.Name, p) {
			return r.Name[len(p):]
		}
	}
	return r.Name
}

// RefUpdate is one compare-and-swap against a reference.
//
// A zero Old means the ref must not exist, which is how a create is
// expressed. A zero New deletes it. Writing both as object names rather than
// as separate operations keeps every update a single atomic comparison.
type RefUpdate struct {
	Name string
	Old  object.ID
	New  object.ID
}

// IsCreate reports whether the update creates a new reference.
func (u RefUpdate) IsCreate() bool { return u.Old.IsZero() && !u.New.IsZero() }

// IsDelete reports whether the update removes a reference.
func (u RefUpdate) IsDelete() bool { return u.New.IsZero() && !u.Old.IsZero() }

// ObjectStore holds git objects, addressed by content.
//
// Objects are immutable and their names are their hashes, so a Put of an
// object that already exists is a no-op rather than a conflict, and the
// store never needs a write lock for reads to be consistent.
type ObjectStore interface {
	// Get returns an object, or ErrNotFound.
	Get(ctx context.Context, id object.ID) (*object.Object, error)
	// Has reports whether an object is present, without reading it.
	Has(ctx context.Context, id object.ID) (bool, error)
	// Put stores an object and returns its name. Storing an object that is
	// already present succeeds.
	Put(ctx context.Context, obj *object.Object) (object.ID, error)
	// PutBatch stores many objects, which lets a backend amortize one
	// transaction or one upload across a whole push.
	PutBatch(ctx context.Context, objs []*object.Object) error
	// Count returns how many objects are stored.
	Count(ctx context.Context) (int64, error)
}

// RefStore holds references, with atomic compare-and-swap updates.
//
// This is the only part of a repository that serializes. Objects are
// immutable and can be written concurrently without coordination, so a fleet
// of agents pushing to distinct branches contends on nothing: each update
// touches its own reference.
type RefStore interface {
	// GetRef returns one reference, or ErrNotFound.
	GetRef(ctx context.Context, name string) (Ref, error)
	// ListRefs returns every reference whose name starts with prefix,
	// sorted by name. An empty prefix lists all of them.
	ListRefs(ctx context.Context, prefix string) ([]Ref, error)
	// UpdateRefs applies updates atomically: either all of them take effect
	// or none do. A failed comparison returns *ErrRefConflict.
	UpdateRefs(ctx context.Context, updates []RefUpdate) error
	// HeadTarget returns the symbolic ref HEAD points at, such as
	// refs/heads/main. It is reported even when that branch does not exist
	// yet, which is what an empty repository looks like.
	HeadTarget(ctx context.Context) (string, error)
	// SetHeadTarget points HEAD at a branch.
	SetHeadTarget(ctx context.Context, target string) error
}

// Store is everything a repository needs.
type Store interface {
	ObjectStore
	RefStore
	Close() error
}

// Memory is an in-memory Store for tests and for running without a database.
//
// It is safe for concurrent use. Reads take a read lock and ref updates take
// the write lock for the whole batch, which is what makes a multi-ref update
// atomic.
type Memory struct {
	mu      sync.RWMutex
	objects map[object.ID]*object.Object
	refs    map[string]object.ID
	head    string
}

// NewMemory returns an empty in-memory store with HEAD on refs/heads/main.
func NewMemory() *Memory {
	return &Memory{
		objects: make(map[object.ID]*object.Object),
		refs:    make(map[string]object.ID),
		head:    "refs/heads/main",
	}
}

var _ Store = (*Memory)(nil)

func (m *Memory) Get(_ context.Context, id object.ID) (*object.Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[id]
	if !ok {
		return nil, fmt.Errorf("%w: object %s", ErrNotFound, id)
	}
	return o, nil
}

func (m *Memory) Has(_ context.Context, id object.ID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.objects[id]
	return ok, nil
}

func (m *Memory) Put(_ context.Context, obj *object.Object) (object.ID, error) {
	id := obj.ID()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[id] = obj
	return id, nil
}

func (m *Memory) PutBatch(_ context.Context, objs []*object.Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range objs {
		m.objects[o.ID()] = o
	}
	return nil
}

func (m *Memory) Count(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.objects)), nil
}

func (m *Memory) GetRef(_ context.Context, name string) (Ref, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	target, ok := m.refs[name]
	if !ok {
		return Ref{}, fmt.Errorf("%w: ref %s", ErrNotFound, name)
	}
	return Ref{Name: name, Target: target}, nil
}

func (m *Memory) ListRefs(_ context.Context, prefix string) ([]Ref, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Ref
	for name, target := range m.refs {
		if strings.HasPrefix(name, prefix) {
			out = append(out, Ref{Name: name, Target: target})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) UpdateRefs(_ context.Context, updates []RefUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check every comparison before applying any of them, so a batch that
	// fails leaves nothing half-written.
	for _, u := range updates {
		actual := m.refs[u.Name] // zero if absent, which is what Old zero means
		if actual != u.Old {
			return &ErrRefConflict{Ref: u.Name, Expected: u.Old, Actual: actual}
		}
	}
	for _, u := range updates {
		if u.New.IsZero() {
			delete(m.refs, u.Name)
		} else {
			m.refs[u.Name] = u.New
		}
	}
	return nil
}

func (m *Memory) HeadTarget(_ context.Context) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.head, nil
}

func (m *Memory) SetHeadTarget(_ context.Context, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.head = target
	return nil
}

func (m *Memory) Close() error { return nil }
