// Package hub defines a dispatcher that consumes, indexes, and distributes
// the output of a feed.
package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tendermint/tendermint/x/feed"
)

// ErrHubRunning is reported by Wait if its context ends before the hub exited.
var ErrHubRunning = errors.New("hub is still running")

// A Hub consumes items from a feed, optionally indexes them, and distributes
// them to a dynamic collection of listeners.
type Hub struct {
	feed  *feed.Feed
	index IndexFunc

	errc chan error
	err  error

	listeners struct {
		// Lock exclusive: Adding or removing listeners in set.
		// Lock shared: Reading the set of active listeners.
		sync.RWMutex
		set map[*Listener]struct{}
	}
}

// New constructs a new, unstarted hub consuming items from f.
// If opts == nil, default options are used as described by Options.
// New will panic if f == nil.
func New(f *feed.Feed, opts *Options) *Hub {
	if f == nil {
		panic("nil hub feed")
	}

	h := &Hub{feed: f, errc: make(chan error, 1), index: opts.indexFunc()}
	h.listeners.set = make(map[*Listener]struct{})
	return h
}

// Run starts the hub and blocks until it exits or ctx ends.
// If the source feed ends, the hub will exit and Run returns nil.
// Otherwise, Run returns the error that caused the hub to exit.
//
// To run the hub concurrently, call h.Run in a goroutine and use the Wait
// method to wait for the exit status.
func (h *Hub) Run(ctx context.Context) error {
	h.errc <- h.feed.Stream(ctx, func(itm feed.Item) error {
		cursor, err := h.index(ctx, itm)
		if err != nil {
			return fmt.Errorf("hub indexer: %w", err)
		}
		h.deliverToListeners(Item{Item: itm, Cursor: cursor})
		return nil
	})
	// The first successful waiter will close h.errc.

	return h.Wait()
}

// Wait blocks until h exits, and reports the exit status of the hub.  Wait
// returns the same value reported by Run.
func (h *Hub) Wait() error {
	if v, ok := <-h.errc; ok {
		// We are the first waiter, so ensure the error channel closes when
		// we're finished to unblock concurrent/subsequent waiters.
		defer close(h.errc)

		// Don't record an error if the reason we exited was the feed ending.
		if !feed.AtEnd(v) {
			h.err = v
		}

		// Unregister all the listeners so they don't get stuck, and remove
		// the registry so that any new listeners will start closed.
		h.listeners.Lock()
		defer h.listeners.Unlock()
		for lst := range h.listeners.set {
			lst.markClosed()
		}
		h.listeners.set = nil
	}
	return h.err
}

// Listen creates and registers a new listener with the given options in h.
// The listener remains registered until it is closed or the hub exits.
//
// If a listener is registered on a hub that has already exited, the listener
// will already be closed when it is returned. Such a listener is usable, but
// will deliver no items.
func (h *Hub) Listen(opts *ListenOptions) *Listener {
	mu := new(sync.Mutex)
	return h.addListener(&Listener{
		hub:       h,
		wantLabel: opts.matchLabel(),
		wantData:  opts.matchData(),

		mu:     mu,
		nempty: sync.NewCond(mu),
		queue:  make([]Item, opts.maxQueueLen()),
	})
}

func (h *Hub) addListener(lst *Listener) *Listener {
	h.listeners.Lock()
	defer h.listeners.Unlock()

	// The registry is set to nil when the hub exits. In this case, mark the new
	// listener as closed immediately.
	if h.listeners.set == nil {
		lst.markClosed()
	} else {
		h.listeners.set[lst] = struct{}{}
	}
	return lst
}

func (h *Hub) removeListener(lst *Listener) {
	h.listeners.Lock()
	defer h.listeners.Unlock()
	delete(h.listeners.set, lst)
}

func (h *Hub) deliverToListeners(itm Item) {
	h.listeners.RLock()
	defer h.listeners.RUnlock()
	for lst := range h.listeners.set {
		lst.accept(itm)
	}
}

// Options provides optional settings for a Hub.
// A nil *HubOptions is ready for use providing default values as described.
type Options struct {
	// If set, the hub will call this function synchronously with each item
	// received from the feed before it is distribute to listeners. If nil,
	// items are not indexed. At most one call to IndexItem will be active at a
	// time from a given hub.
	IndexItem IndexFunc
}

func (o *Options) indexFunc() IndexFunc {
	if o == nil || o.IndexItem == nil {
		return func(context.Context, feed.Item) (string, error) { return "", nil }
	}
	return o.IndexItem
}

// An IndexFunc records the given item in the index and returns an opaque
// string cursor that can be used to retrieve the item. The structure of a
// cursor is specific to the indexer, and is not interpreted by the hub.
//
// Cursor values are valid for the lifetime of the index that created them, and
// can be stored or exchanged with callers who need to query the index.  For
// encoding into a wire format, a cursor should be treated as a binary blob.
type IndexFunc func(context.Context, feed.Item) (string, error)
