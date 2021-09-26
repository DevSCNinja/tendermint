// Package memstore implements a trivial in-memory index.Store.
package memstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/tendermint/tendermint/x/index"
)

// Store implements the index.Store interface using plain data values cached in
// an in-memory slice. The zero value is ready for use, but must not be copied.
type Store struct {
	// Lock exclusive: Add a new entry, prune old entries.
	// Lock shared: Read the entries slice.
	//
	// Once the reader has captured the entries slice, the lock can be released.
	// The slice will remain valid.

	mu      sync.RWMutex
	entries []*entry // oldest first
}

// New returns a new empty Store.
func New() *Store { return new(Store) }

// Len reports the number of entries currently indexed.
func (s *Store) Len() int { return len(s.getEntries()) }

// Prune removes as many old entries from s as necessary to reduce the total
// number of indexed entries to no greater than n. It returns the number of
// entries discarded.
func (s *Store) Prune(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) > n {
		// Copy the data to a new slice, so we don't pin the old array.
		rm := len(s.entries) - n
		pruned := make([]*entry, n)
		copy(pruned, s.entries[rm:])
		s.entries = pruned
		return rm
	}
	return 0
}

type entry struct {
	cursor string
	label  string
	data   interface{}
}

func (s *Store) getEntries() []*entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries
}

func (s *Store) addEntry(e *entry) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.cursor = fmt.Sprintf("%08x", len(s.entries)+1)
	s.entries = append(s.entries, e)
	return e.cursor
}

// Put stores the specified item in the index, as index.Store.  Items are
// stored literally and not encoded. This implementation of Put never reports
// an error.
func (s *Store) Put(_ context.Context, _ uint64, label string, data interface{}) (string, error) {
	if label == "" && data == nil {
		return "", errors.New("invalid index item")
	}
	return s.addEntry(&entry{
		label: label,
		data:  data,
	}), nil
}

// Span reports the oldest and newest cursors stored in s, as index.Store.
func (s *Store) Span(_ context.Context) (oldest, newest string, _ error) {
	es := s.getEntries()
	if len(es) == 0 {
		return "", "", index.ErrSpanEmpty
	}
	return es[0].cursor, es[len(es)-1].cursor, nil
}

// Scan delivers each matching entry to f, as index.Store.
func (s *Store) Scan(ctx context.Context, q index.ScanQuery, f func(index.Entry) error) error {
	if q.Start == "" || q.Limit == "" {
		lo, hi, err := s.Span(ctx)
		if err != nil {
			return nil // exit early: nothing is indexed
		}
		if q.Start == "" {
			q.Start = lo
		}
		if q.Limit == "" {
			q.Limit = hi
		}
	}
	if q.Limit < q.Start {
		return nil // exit early: range is empty
	}

	es := s.getEntries()
	i := sort.Search(len(es), func(pos int) bool {
		return es[pos].cursor >= q.Start
	})
	if i == len(es) {
		return nil // no matching entries
	}
	j := sort.Search(len(es), func(pos int) bool {
		return es[pos].cursor > q.Limit
	})
	if j <= i {
		return nil // no matching entries

		// If this happens it probably means the caller reversed cursors, but
		// they'll get their comeuppance when they don't get any results.
	}
	for _, e := range es[i:j] {
		if q.MatchLabel != nil && !q.MatchLabel(e.label) {
			continue
		}
		if err := f(index.Entry{
			Cursor: e.cursor,
			Label:  e.label,
			Data:   e.data,
		}); err != nil {
			return err
		}
	}
	return nil
}
