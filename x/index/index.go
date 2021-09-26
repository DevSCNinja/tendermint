// Package index defines an indexing interface for feed.Item values.
package index

import (
	"context"
	"errors"

	"github.com/tendermint/tendermint/x/feed"
)

var (
	// ErrStopReading is returned by the callback of a Read method to indicate
	// that the read should be ended without error.
	ErrStopReading = errors.New("stop reading")

	// ErrSpanEmpty is returned when the caller requests a span of the index
	// that contains no entries.
	ErrSpanEmpty = errors.New("span is empty")
)

// Store is the interface to underlying indexing storage. The implementation
// controls how data are encoded and stored, but must be able to support a view
// of data that reflects the order in which items were written.
//
// Writing an item to the store assigns an opaque string cursor for the item.
// Cursors are not required to be ordered, meaning that the caller cannot
// necessarily compare cursor strings to find out which item was indexed first.
// The store is free to issue cursors that allow that comparison for its own
// internal use, howeever.
type Store interface {
	// Put writes the specified item to storage and returns a unique, non-empty
	// cursor for it.  It is an error if both label == "" and data == nil.
	// Each call to Put must write a new record, even if the arguments are
	// identical to a previous call.
	Put(ctx context.Context, id uint64, label string, data interface{}) (string, error)

	// Span reports the oldest and newest cursor values available in the index.
	// If no items are stored, Span reports ErrSpanEmpty.
	Span(ctx context.Context) (oldest, newest string, _ error)

	// Scan calls f with each indexed item matching the specified query, in the
	// order in which they were stored.  The scan stops when there are no
	// further items, or when f reports an error. If f reports an error, Scan
	// returns that error, otherwise nil.
	//
	// If the start and limit cursors in the query do not exist in the store,
	// Scan may either return no results (without error) or, if its cursor
	// format allows comparison, return all the results in the index with
	// cursors in the closed interval bounded by the start and limit.  In either
	// case, Scan may not report an error for missing start or limit keys.
	Scan(ctx context.Context, q ScanQuery, f func(Entry) error) error
}

// A ScanQuery carries the parameters to a storage Scan.
type ScanQuery struct {
	// Match items at or after this cursor. If Start == "", the oldest cursor at
	// the point when the scan begins is used.
	Start string

	// Match items at or before the item with this cursor. If Limit == "", the
	// newest cursor at the point when the scan begins is used.
	Limit string

	// Report only items for which this predicate returns true on the label.
	// If nil, report all items regardless of label.
	MatchLabel func(string) bool
}

// An Entry is the argument to the callback of a store's Scan method.
type Entry struct {
	Cursor string      // the storage cursor for this item
	Label  string      // the item label (may be "")
	Data   interface{} // the item data (may be nil)

	// N.B. An entry does not include the ID. ID values are provided to the
	// store as an aid to generating a cursor, but the value is meaningless
	// outside the feed that generated the item, so the store is not required to
	// persist the value explicitly.
}

// A Writer indexes items to an underlying index store.
type Writer struct{ store Store }

// NewWriter returns a Writer that delegates to the given Store.
func NewWriter(s Store) Writer { return Writer{store: s} }

// Write adds itm to the store and returns its cursor.
func (w Writer) Write(ctx context.Context, itm feed.Item) (string, error) {
	return w.store.Put(ctx, itm.ID(), itm.Label, itm.Data)
}

// A Reader retrieves items from an underlying index store.
type Reader struct{ store Store }

// NewReader returns a Reader that delegates to the given Store.
func NewReader(s Store) Reader { return Reader{store: s} }

// Span returns the oldest and newest cursors indexed by the store, or returns
// ErrSpanEmpty if no data are indexed.
func (r Reader) Span(ctx context.Context) (oldest, newset string, err error) {
	return r.store.Span(ctx)
}

// Read calls f for each indexed item matching the specified query, in the
// order in which they were stored.
//
// If no items match the query, Read returns ErrSpanEmpty.  Otherwise, reading
// stops when there are no further items, or when f reports an error. If f
// returns ErrStopReading, Read returns nil; otherwise it returns the error
// reported by f.
func (r Reader) Read(ctx context.Context, q ReadQuery, f func(Entry) error) error {
	sq := ScanQuery{
		Start:      q.Start,
		Limit:      q.Limit,
		MatchLabel: q.MatchLabel,
	}

	empty := true
	if err := r.store.Scan(ctx, sq, func(entry Entry) error {
		if !q.wantData(entry.Data) {
			return nil
		}
		empty = false
		return f(entry)
	}); err == ErrStopReading {
		return nil
	} else if err != nil {
		return err
	} else if empty {
		return ErrSpanEmpty
	}
	return nil
}

// A ReadQuery carries the parameters to a Read operation.
type ReadQuery struct {
	// Match items at or after this cursor. If Start == "", the oldest cursor at
	// the point when the read begins is used.
	Start string

	// Match items at or before the item with this cursor. If Limit == "", the
	// newest cursor at the point when the read begins is used.
	Limit string

	// Report only items for which this predicate returns true on the label.
	// If nil, report all items regardless of label.
	MatchLabel func(label string) bool

	// Report only items for which this predicate returns true on the data.
	// If nil, report all items regardless of data.
	// This predicate is checked only after filtering by label.
	MatchData func(data interface{}) bool
}

func (rq ReadQuery) wantData(data interface{}) bool {
	if rq.MatchData == nil {
		return true
	}
	return rq.MatchData(data)
}
