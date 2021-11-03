// Package feed implements a buffered sequential feed of data items.
package feed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	// ErrFeedClosed is returned by Add when an item is added after the feed has
	// been closed.
	ErrFeedClosed = errors.New("feed is closed")

	errEndOfFeed = errors.New("end of feed")
)

// AtEnd reports whether err is the error returned by Next to signal that the
// feed has no further elements.
func AtEnd(err error) bool { return errors.Is(err, errEndOfFeed) }

// A Feed is a buffered, ordered sequence of data items.
// After construction, all the methods of a feed are safe for concurrent use by
// multiple goroutines. The caller must ensure the Close method is called when
// the feed is no longer in use.
type Feed struct {
	lastID uint64 // atomic

	queue chan Item
	done  <-chan struct{} // context channel
	stop  func()          // call to request shutdown (repeated calls OK)
	adds  sync.RWMutex    // exclusive: shutdown; shared: add
}

// New constructs a new Feed with buffer capacity for n ≥ 0 items, using the
// given options. If opts == nil, default settings are used as described on the
// Options type. The feed runs until it is closed.
//
// If n == 0, the Feed is unbuffered, and each Add call must rendezvous with a
// corresponding Next call. If n < 0, New will panic.
func New(n int, opts *Options) *Feed {
	if n < 0 {
		panic("negative feed capacity")
	}

	// Feed operations are affected by two contexts:
	// The context constructed here governs the lifetime of the feed itself.
	// The context passed to Add/Next governs the lifetime of that call.
	ctx, cancel := context.WithCancel(context.Background())
	f := &Feed{
		queue: make(chan Item, n),
		done:  ctx.Done(),
		stop:  cancel,
	}
	go func() {
		<-ctx.Done()
		f.adds.Lock()
		defer f.adds.Unlock()
		close(f.queue)
	}()
	return f
}

// Add adds the given item to s, blocking until either the item is added or
// until ctx ends. Each item is assigned a positive sequence ID when it is
// added to the feed. At least one of the Label and Data fields of the item
// must be non-empty. It returns ErrFeedClosed if f is closed.
func (f *Feed) Add(ctx context.Context, item Item) error {
	if item.Label == "" && item.Data == nil {
		return errors.New("invalid empty item")
	}
	f.adds.RLock()
	defer f.adds.RUnlock()

	item.id = atomic.AddUint64(&f.lastID, 1)
	select {
	case <-f.done:
		return ErrFeedClosed
	default:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f.queue <- item:
			return nil
		}
	}
}

// Next removes and returns the next available item from s, blocking until an
// item is available, the feed closes, or ctx ends.
func (f *Feed) Next(ctx context.Context) (Item, error) {
	select {
	case <-ctx.Done():
		return Item{}, ctx.Err()
	case item, ok := <-f.queue:
		if !ok {
			return Item{}, errEndOfFeed
		}
		return item, nil
	}
}

// Stream removes and calls cb with each available item from s until the stream
// closes, ctx ends, or f reports an error. If f reports an error or the
// context ends, the corresponding error is returned to the caller of Stream.
// If the feed is fully consumed, Stream returns nil.
func (f *Feed) Stream(ctx context.Context, cb func(Item) error) error {
	for {
		item, err := f.Next(ctx)
		if AtEnd(err) {
			return nil
		} else if err != nil {
			return err
		} else if err := cb(item); err != nil {
			return err
		}
	}
}

// Close closes the feed, after which no further items may be added.  Any
// undelivered items remaining in the feed will be delivered.
func (f *Feed) Close() error { f.stop(); return nil }

// Options define construction options for a feed. A nil *Options is ready for
// use and provides default values as described.
type Options struct {
	// There are no options yet, this is a placeholder.
}

// An Item is a single element of a feed. Each item has a string label and an
// optional opaque data value, at least one of which must be non-empty for an
// item to be valid. Each item is assigned a positive ID value when it is added
// to the feed.
type Item struct {
	Label string
	Data  interface{}

	id uint64
}

// ID reports the ID assigned to the item, or 0 for an unsent item.
func (it Item) ID() uint64 { return it.id }
