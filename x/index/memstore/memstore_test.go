package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tendermint/tendermint/x/index"
	"github.com/tendermint/tendermint/x/index/memstore"
	"github.com/tendermint/tendermint/x/match"
)

var _ index.Store = (*memstore.Store)(nil)

func TestStore(t *testing.T) {
	s := memstore.New()

	ctx := context.Background()

	// Span: Reports ErrSpanEmpty if nothing is indexed.
	t.Run("SpanEmpty", func(t *testing.T) {
		if lo, hi, err := s.Span(ctx); err != index.ErrSpanEmpty {
			t.Errorf("Span empty: got (%q, %q, %v), want %v", lo, hi, err, index.ErrSpanEmpty)
		}
	})

	// Put: It is an error if label and data are both empty.
	t.Run("PutInvalid", func(t *testing.T) {
		if c, err := s.Put(ctx, 1, "", nil); err == nil {
			t.Errorf("Put empty: got %q, wanted error", c)
		}
	})

	// Put: Duplicate arguments are indexed separately.
	c1, _ := s.Put(ctx, 101, "foo", nil)
	c2, _ := s.Put(ctx, 101, "foo", nil)
	if n := s.Len(); n != 2 {
		t.Errorf("s.Len() = %d, want %d", n, 2)
	}
	if c1 == c2 {
		t.Errorf("Identical cursors for different inserts: %q", c1)
	}

	c3, _ := s.Put(ctx, 200, "blah", 25)

	// Span: Reports the known endpoints if something is indexed.
	t.Run("SpanNonEmpty", func(t *testing.T) {
		if lo, hi, err := s.Span(ctx); err != nil {
			t.Errorf("Span: unexpected error: %v", err)
		} else if lo != c1 || hi != c3 {
			t.Errorf("Span: got (%q, %q) want (%q, %q)", lo, hi, c1, c3)
		}
	})

	// Scan: Empty endpoints scan the entire range.
	t.Run("ScanAll", func(t *testing.T) {
		var numEntries int
		if err := s.Scan(ctx, index.ScanQuery{}, func(e index.Entry) error {
			numEntries++
			want := fmt.Sprintf("%08x", numEntries)
			if e.Cursor != want {
				t.Errorf("Entry %+v cursor: got %q, want %q", e, e.Cursor, want)
			}
			return nil
		}); err != nil {
			t.Errorf("Scan: unexpected error: %v", err)
		}
		if numEntries != s.Len() {
			t.Errorf("Scan: got %d entries, want %d", numEntries, s.Len())
		}
	})

	// Scan: Filter by label.
	t.Run("ScanFiltered", func(t *testing.T) {
		const want = "foo"
		q := index.ScanQuery{MatchLabel: match.Exactly(want)}

		var numEntries int
		if err := s.Scan(ctx, q, func(e index.Entry) error {
			if e.Label != want {
				t.Errorf("Entry %+v label: got %q, want %q", e, e.Label, want)
			} else {
				numEntries++
			}
			return nil
		}); err != nil {
			t.Errorf("Scan: unexpected error: %v", err)
		}
		if numEntries != 2 {
			t.Errorf("Scan filtered: got %d entries, want 2", numEntries)
		}
	})

	// Scan: Callback errors terminate scanning.
	t.Run("ScanCallbackError", func(t *testing.T) {
		cbErr := errors.New("totally bogus")
		if err := s.Scan(ctx, index.ScanQuery{}, func(e index.Entry) error {
			return cbErr
		}); err != cbErr {
			t.Errorf("Scan: got err %v, want %v", err, cbErr)
		}
	})

	t.Run("ScanPartialRange", func(t *testing.T) {
		var numEntries int
		if err := s.Scan(ctx, index.ScanQuery{Start: c2}, func(index.Entry) error {
			numEntries++
			return nil
		}); err != nil {
			t.Errorf("Scan: unexpected error: %v", err)
		}
		if numEntries != 2 {
			t.Errorf("Scan partial: got %d entries, want 2", numEntries)
		}
	})

	t.Run("ScanEmptyRange", func(t *testing.T) {
		if err := s.Scan(ctx, index.ScanQuery{Start: "Z", Limit: "A"}, func(e index.Entry) error {
			t.Errorf("Scan: unexpected entry: %+v", e)
			return nil
		}); err != nil {
			t.Errorf("Scan: unexpected error: %v", err)
		}
	})
}
