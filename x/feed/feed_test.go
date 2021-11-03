package feed_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sync/errgroup"

	"github.com/tendermint/tendermint/x/feed"
)

func TestAddContext(t *testing.T) {
	f := feed.New(0, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := f.Add(ctx, feed.Item{Label: "foo"}); err != context.DeadlineExceeded {
		t.Errorf("Add: reported %v, wanted context termination", err)
	}
}

func TestRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The test inputs are plumbed in as item labels.
	// The feed is given less capacity than the total feed length, so that
	// we exercise producer/consumer synchronization.
	words := []string{"many", "bothans", "died", "to", "bring", "us", "these", "plans"}
	ft := newFeedTester(t, ctx, feed.New(len(words)/2, nil))

	// Consumer: Accumulate the item labels for comparison.
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)

		// Verify that item IDs are positive and increasing.
		var lastID uint64

		if err := ft.Stream(ctx, func(item feed.Item) error {
			id := item.ID()

			if id <= lastID {
				return fmt.Errorf("item id=%d label=%q data=%v out of order: lastID=%d",
					id, item.Label, item.Data, lastID)
			}

			got = append(got, item.Label)
			return nil
		}); err != nil {
			t.Errorf("Stream failed: %v", err)
		}
	}()

	// Producer: Send item labels and arbitrary (integer) data.
	for i, w := range words {
		ft.mustAdd(w, 3*i+1)
	}
	ft.Close()
	<-done

	if diff := cmp.Diff(words, got); diff != "" {
		t.Errorf("Wrong result: (-want, +got)\n%s", diff)
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	ft := newFeedTester(t, ctx, feed.New(5, nil))

	// An empty item should report an error from Add.
	if err := ft.Add(ctx, feed.Item{}); err == nil {
		t.Error("Add of an empty item unexpectedly succeeded")
	}

	// Add some items to the feed, and verify that we can get them back out
	// while the feed is still open.
	ft.mustAdd("A", "foo")
	ft.mustAdd("B", "bar")
	if itm := ft.mustNext(); itm.Label != "A" {
		t.Errorf("First item: got %q, want A", itm.Label)
	}

	// Now close the feed and verify that we can still get the reamining items
	// that were already added, but can no longer add any new ones.
	ft.Close()

	if err := ft.Add(ctx, feed.Item{Label: "C"}); err == nil {
		t.Error("Add succeeded after close")
	}
	if itm := ft.mustNext(); itm.Label != "B" {
		t.Errorf("Second item: got %q, want B", itm.Label)
	}

	// Now that we have read everything we wrote, Next should fail.
	if itm, err := ft.Next(ctx); !feed.AtEnd(err) {
		t.Errorf("Next at end: got %+v, %v; wanted EOF", itm, err)
	}
}

// Exercise concurrent readers and writers on a single feed.
func TestConcurrent(t *testing.T) {
	ctx := context.Background()
	ft := newFeedTester(t, ctx, feed.New(5, nil))

	const itemsToAdd = 1000

	// Transmit: Two concurrent goroutines sending to the feed.
	var tg errgroup.Group
	tg.Go(func() error {
		for i := 0; i < itemsToAdd; i++ {
			ft.mustAdd("t1:"+strconv.Itoa(i+1), nil)
		}
		return nil
	})
	tg.Go(func() error {
		for i := 0; i < itemsToAdd; i++ {
			ft.mustAdd("t2:"+strconv.Itoa(i+1), nil)
		}
		return nil
	})
	tg.Go(func() error {
		for i := 0; i < itemsToAdd; i++ {
			ft.mustAdd("t3:"+strconv.Itoa(i+1), nil)
		}
		return nil
	})

	// Receive: Two concurrent goroutines receiving from the feed.
	var rg errgroup.Group
	var r1got []feed.Item
	rg.Go(func() error {
		return ft.Stream(ctx, func(it feed.Item) error {
			r1got = append(r1got, it)
			return nil
		})
	})
	var r2got []feed.Item
	rg.Go(func() error {
		return ft.Stream(ctx, func(it feed.Item) error {
			r2got = append(r2got, it)
			return nil
		})
	})

	// After transmission, close the feed and wait for the receivers to settle.
	if err := tg.Wait(); err != nil {
		t.Errorf("Transmit wait: %v", err)
	}
	ft.Close()
	if err := rg.Wait(); err != nil {
		t.Errorf("Receive wait: %v", err)
	}

	total := len(r1got) + len(r2got)
	if want := 3 * itemsToAdd; total != want {
		t.Errorf("Received %d items, wanted %d", total, want)
	}
}

func TestConcurrentStop(t *testing.T) {
	ctx := context.Background()
	ft := newFeedTester(t, ctx, feed.New(5, nil))

	const itemsToAdd = 1000

	// This test closes the feed while items are being added, to verify that the
	// adder correctly handles that case and does not race with shutdown.
	var tg errgroup.Group
	for _, s := range []string{"A", "B", "C"} {
		label := s + ":"
		tg.Go(func() error {
			for i := 0; i < itemsToAdd; i++ {
				if err := ft.Add(ctx, feed.Item{Label: label + strconv.Itoa(i+1)}); err != nil {
					return err
				}
			}
			return nil
		})
	}

	var numReceived int
	if err := ft.Stream(ctx, func(it feed.Item) error {
		numReceived++
		if numReceived == itemsToAdd/4 {
			ft.Close()
		}
		return nil
	}); err != nil {
		t.Errorf("Stream: unexpected error: %v", err)
	}
	if err := tg.Wait(); err != feed.ErrFeedClosed {
		t.Errorf("Adder: got %v, want %v", err, feed.ErrFeedClosed)
	}
	t.Logf("Received %d of %d items before close", numReceived, itemsToAdd)
}

type feedTester struct {
	*feed.Feed
	ctx context.Context
	t   *testing.T
}

func newFeedTester(t *testing.T, ctx context.Context, f *feed.Feed) feedTester {
	return feedTester{Feed: f, ctx: ctx, t: t}
}

func (f feedTester) mustAdd(label string, value interface{}) {
	f.t.Helper()
	if err := f.Add(f.ctx, feed.Item{
		Label: label,
		Data:  value,
	}); err != nil {
		f.t.Errorf("Add(%q): unexpected error: %v", label, err)
	}
}

func (f feedTester) mustNext() feed.Item {
	f.t.Helper()
	itm, err := f.Next(f.ctx)
	if err != nil {
		f.t.Fatalf("Next failed: %v", err)
	}
	return itm
}
