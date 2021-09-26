package hub

import (
	"context"
	"errors"
	"sync"

	"github.com/tendermint/tendermint/x/feed"
	"github.com/tendermint/tendermint/x/match"
)

// An Item combines a feed.Item with an indexing cursor.
type Item struct {
	feed.Item

	Cursor string
}

var (
	// ErrQueueEmpty is returned by TryNext if there are no items available from
	// the listener.
	ErrQueueEmpty = errors.New("queue is empty")

	// ErrListenerClosed is returned by Next or TryNext if the listener is
	// closed and no further items are available.
	ErrListenerClosed = errors.New("listener is closed")
)

// A Listener receives items from a Hub. Use the Next and TryNext methods to
// receive items from the listener.
//
// A Listener does not block on items from the hub: Undelivered items are
// queued up to a fixed queue size, and then evicted.  By default the oldest
// undelivered item is evicted, but the caller may customize the eviction
// policy by providing an EvictFunc when the Listener is constructed.
type Listener struct {
	// The hub that created this listener, for closing.
	hub       *Hub
	wantLabel func(string) bool
	wantData  func(interface{}) bool

	mu     *sync.Mutex // protects the fields below
	nempty *sync.Cond  // condition: queue is not empty
	queue  []Item      // items awaiting delivery (ring buffer)
	qnext  int         // the next available slot in queue
	qfront int         // the oldest in-use slot in the queue
	qsize  int         // the number of elements in the queue
	lost   int         // count of evictions
	closed bool        // true when the listener has closed
}

// Next returns the next item from lst. It blocks until an item is available or
// until ctx ends. Next returns ErrListenerClosed if lst is closed and no
// further items are available.
func (lst *Listener) Next(ctx context.Context) (Item, error) {
	lst.mu.Lock()
	defer lst.mu.Unlock()

	// Shortcut for a closed listener, skip the wait plumbing.
	if lst.qsize == 0 && lst.closed {
		return Item{}, ErrListenerClosed
	}

	// Ensure the context waiter gets cleaned up when we're done.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Wake up if the context ends.
	var isDone bool
	go func() { <-ctx.Done(); isDone = true; lst.nempty.Broadcast() }()

	for lst.qsize == 0 {
		if lst.closed {
			// The listener was closed while we were waiting, give up.
			return Item{}, ErrListenerClosed
		} else if isDone {
			// The context terminated before we got anything.
			return Item{}, ctx.Err()
		}
		lst.nempty.Wait()
	}
	return lst.popOldest(), nil
}

// TryNext returns the next item from lst, if any are available.
// If no items are available, TryNext returns ErrListenerClosed if the
// listener is closed, otherwise it returns ErrQueueEmpty.
// This method does not block; use Next to wait for an item.
func (lst *Listener) TryNext() (Item, error) {
	lst.mu.Lock()
	defer lst.mu.Unlock()

	if lst.qsize == 0 {
		if lst.closed {
			return Item{}, ErrListenerClosed
		}
		return Item{}, ErrQueueEmpty
	}
	return lst.popOldest(), nil
}

// Lost reports the number of items that have been evicted from the queue since
// the last call to Lost. Each call to Lost resets the counter to 0.
func (lst *Listener) Lost() int {
	lst.mu.Lock()
	defer lst.mu.Unlock()
	n := lst.lost
	lst.lost = 0
	return n
}

// Close unregisters the listener from its hub and returns nil.  After calling
// close, lst will receive no new items from the hub, but any undelivered items
// that were already queued remain available.
func (lst *Listener) Close() error {
	lst.hub.removeListener(lst)
	lst.markClosed()
	return nil
}

// markClosed sets the closed flag for lst. This is separated from Close so
// that it can be used by the hub during shutdown.
func (lst *Listener) markClosed() {
	lst.mu.Lock()
	defer lst.mu.Unlock()
	lst.nempty.Broadcast()
	lst.closed = true
}

// popOldest removes and returns the oldest item in the queue.
// Preconditions: Caller holds lst.mu, and lst.qsize > 0.
func (lst *Listener) popOldest() Item {
	out := lst.queue[lst.qfront]
	lst.qfront = (lst.qfront + 1) % len(lst.queue)
	lst.qsize--
	return out
}

// accept adds incoming to the queue, handling eviction as necessary.
func (lst *Listener) accept(incoming Item) {
	if !lst.wantLabel(incoming.Label) || !lst.wantData(incoming.Data) {
		return // not interested in this item
	}

	lst.mu.Lock()
	defer lst.mu.Unlock()

	if lst.qsize == len(lst.queue) {
		// The queue is full, evict the oldest undelivered item.
		lst.qfront = (lst.qfront + 1) % len(lst.queue)
		lst.qsize--
		lst.lost++

		// fall through to insert normally
	}

	lst.queue[lst.qnext] = incoming
	lst.qnext = (lst.qnext + 1) % len(lst.queue)
	lst.qsize++

	// If we just made the queue nonempty, signal the condition.
	if lst.qsize == 1 {
		lst.nempty.Broadcast()
	}
}

// ListenOptions are optional settings for a listener.
// A nil *ListenOptions is ready for use and provides default values as
// described.
type ListenOptions struct {
	// The maximum number of undelivered items that can be queued.
	// Items in excess of this will be evicted. Zero or negative means 1.
	MaxQueueLen int

	// If set, only enqueue items whose label matches this predicate.
	// If nil, items are not filtered by label.
	MatchLabel func(label string) bool

	// If set, only enqueue items whose data matches this predicate.
	// If nil, items are not filtered by data.
	// This predicate is checked only after filtering by label.
	MatchData func(data interface{}) bool
}

func (o *ListenOptions) maxQueueLen() int {
	if o == nil || o.MaxQueueLen <= 0 {
		return 1
	}
	return o.MaxQueueLen
}

func (o *ListenOptions) matchLabel() func(string) bool {
	if o == nil || o.MatchLabel == nil {
		return match.Any
	}
	return o.MatchLabel
}

func (o *ListenOptions) matchData() func(interface{}) bool {
	if o == nil || o.MatchData == nil {
		return func(interface{}) bool { return true }
	}
	return o.MatchData
}
