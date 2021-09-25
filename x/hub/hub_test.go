package hub_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/tendermint/tendermint/x/feed"
	"github.com/tendermint/tendermint/x/hub"
	"github.com/tendermint/tendermint/x/match"
)

func TestHub_Run(t *testing.T) {
	h := hub.New(feed.New(1, nil), nil)

	ctx, cancelHub := context.WithCancel(context.Background())

	// Run the hub until cancelled, and compare the error it reports to the
	// error from an explicit Wait, to verify they are the same.
	errc := make(chan error, 1)
	go func() {
		defer close(errc)
		errc <- h.Run(ctx)
	}()

	// Check that once the hub exits, Wait and Run return the same error.
	t.Run("WaitMatchesRun", func(t *testing.T) {
		cancelHub()
		werr := h.Wait()
		rerr := <-errc

		if werr != rerr {
			t.Errorf("Error mismatch: from Wait: %v, from Run: %v", werr, rerr)
		}
		if werr != context.Canceled {
			t.Errorf("Wrong error: got %v, want %v", werr, context.Canceled)
		}
	})
}

func TestHub_indexItems(t *testing.T) {
	f := feed.New(1, nil)

	var indexed []string
	h := hub.New(f, &hub.Options{
		IndexItem: func(_ context.Context, itm feed.Item) (string, error) {
			indexed = append(indexed, itm.Label)
			return itm.Label + "/cursor", nil
		},
	})

	ctx := context.Background()
	go h.Run(ctx)

	// Write a bunch of items into the feed and verify that they were indexed in
	// the correct order.
	want := []string{"Zuul", "Asha", "Cleo", "Baxter", "Athena", "Anathema", "Tim"}
	for _, cat := range want {
		f.Add(ctx, feed.Item{Label: cat})
	}
	f.Close()

	// Verify that the hub stopped without error because the feed ended.
	if err := h.Wait(); err != nil {
		t.Errorf("Wait: unexpected error: %v", err)
	}

	if diff := cmp.Diff(want, indexed); diff != "" {
		t.Errorf("Wrong result: (-want, +got)\n%s", diff)
	}
}

func TestListener(t *testing.T) {
	f := feed.New(1, nil) // N.B. Capacity matters

	// Plumb in a no-op indexer that marks added done for each key it indexes,
	// so we can synchronize on additions.
	var added sync.WaitGroup
	h := hub.New(f, &hub.Options{
		IndexItem: func(context.Context, feed.Item) (string, error) {
			added.Done()
			return "", nil
		},
	})

	// Clean up the hub when the test ends.
	defer func() {
		f.Close()
		if err := h.Wait(); err != nil {
			t.Errorf("Wait: unexpected error: %v", err)
		}
	}()

	ctx := context.Background()
	go h.Run(ctx)

	// Add a listener with a 3-position buffer.
	lst := h.Listen(&hub.ListenOptions{MaxQueueLen: 3})
	checkNext := func(t *testing.T, lst *hub.Listener, want string) {
		t.Helper()
		itm, err := lst.Next(ctx)
		if err != nil {
			t.Fatalf("Next: unexpected error: %v", err)
		} else if itm.Label != want {
			t.Errorf("Next: got %q, want %q", itm.Label, want)
		}
	}
	addAll := func(t *testing.T, lst *hub.Listener, labels ...string) {
		t.Helper()
		added.Add(len(labels))
		for _, label := range labels {
			if err := f.Add(ctx, feed.Item{Label: label}); err != nil {
				t.Fatalf("Add %q: unexpected error: %v", label, err)
			}
		}
		added.Wait()
	}

	// Verify that a single item round-trips.
	t.Run("RoundTrip", func(t *testing.T) {
		addAll(t, lst, "1")
		checkNext(t, lst, "1")
	})

	// Add three items, filling the listener queue, and verify that we get them
	// back out in the correct order, none lost.
	t.Run("FillQueue", func(t *testing.T) {
		addAll(t, lst, "P", "D", "Q")
		checkNext(t, lst, "P")
		checkNext(t, lst, "D")
		checkNext(t, lst, "Q")
	})

	// Add four items, causing an eviction. Since we made the listener with the
	// default policy, verify that it was the oldest that got evicted, and that
	// we only wound up with three items in the queue.
	t.Run("OverFillQueue", func(t *testing.T) {
		addAll(t, lst, "A", "B", "C", "D")
		checkNext(t, lst, "B")
		checkNext(t, lst, "C")
		checkNext(t, lst, "D")

		// Verify that the loss counter correctly reflects the eviction.
		if n := lst.Lost(); n != 1 {
			t.Errorf("Lost (first): got %d, want 1", n)
		}

		// Verify that calling Lost reset the counter.
		if n := lst.Lost(); n != 0 {
			t.Errorf("Lost (second): got %d, want 0", n)
		}
	})

	// TryNext on an empty (unclosed) queue should report the correct error.
	t.Run("TryNextEmpty", func(t *testing.T) {
		it, err := lst.TryNext()
		if err != hub.ErrQueueEmpty {
			t.Errorf("TryNext: got %+v, %v; want nil, %v", it, err, hub.ErrQueueEmpty)
		}
	})

	// Add items, close the listener, and verify that the items added before the
	// close are still readable. Check also that items added after the close do
	// not show up.
	t.Run("CloseListener", func(t *testing.T) {
		addAll(t, lst, "X", "Y", "Z")
		checkNext(t, lst, "X")
		lst.Close()

		// Items added before close still appear.
		checkNext(t, lst, "Y")
		checkNext(t, lst, "Z")

		// Items added after close do not appear.
		addAll(t, lst, "blargh")
		if it, err := lst.Next(ctx); err != hub.ErrListenerClosed {
			t.Errorf("Next after close: got %+v, %v, want nil, %v", it, err, hub.ErrListenerClosed)
		}
	})

	// Close a listener while it's waiting, and verify that it finds out.
	t.Run("CloseWaitingListener", func(t *testing.T) {
		lst := h.Listen(nil)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		ready := make(chan struct{}) // closed when call is starting
		done := make(chan struct{})  // closed when call is done
		go func() {
			defer close(done)
			close(ready)
			_, err := lst.Next(ctx)
			if err != hub.ErrListenerClosed {
				t.Errorf("Next: got err=%v, want %v", err, hub.ErrListenerClosed)
			}
		}()

		// Wait for the caller to start, then close the listener, which should
		// promptly report that it is closed.
		<-ready
		lst.Close()

		select {
		case <-done:
			// all is well
		case <-ctx.Done():
			t.Error("Timed out waiting for Next on a closed listener to fail")
		}
	})

	// TryNext on an empty (closed) queue should report the correct error.
	t.Run("TryNextClosed", func(t *testing.T) {
		it, err := lst.TryNext()
		if err != hub.ErrListenerClosed {
			t.Errorf("TryNext: got %+v, %v; want nil, %v", it, err, hub.ErrListenerClosed)
		}
	})

	// Verify that label filters work.
	t.Run("LabelFilter", func(t *testing.T) {
		m := match.Glob("*magic*")
		lst := h.Listen(&hub.ListenOptions{MatchLabel: m, MaxQueueLen: 3})
		addAll(t, lst, "dung", "apples", "magic", "gold", "magical", "corn", "nonmagical")

		checkNext(t, lst, "magic")
		checkNext(t, lst, "magical")
		checkNext(t, lst, "nonmagical")
		if n := lst.Lost(); n != 0 {
			t.Errorf("Lost (filtered): got %d, want 0", n)
		}
	})
}
