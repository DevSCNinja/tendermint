package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/code"
	"github.com/creachadair/jrpc2/handler"
	"github.com/google/uuid"

	"github.com/tendermint/tendermint/x/feed"
	"github.com/tendermint/tendermint/x/hub"
	"github.com/tendermint/tendermint/x/index"
	"github.com/tendermint/tendermint/x/match"
)

const (
	metricBeatInterval = "demo.heartbeatInterval"
	metricEventsPosted = "feed.eventsPosted"
	metricHubListeners = "hub.listeners"
)

// service exports JSON-RPC methods to use a feed, hub, and listeners.
type service struct {
	feed  *feed.Feed
	hub   *hub.Hub
	index index.Reader
	hbeat *time.Ticker

	subs struct {
		sync.Mutex
		m map[string]*hub.Listener
	}
}

func initService(base *service) *service {
	base.subs.m = make(map[string]*hub.Listener)
	return base
}

// methods returns a method assigner for the service.
func (s *service) methods() handler.Map {
	return handler.Map{
		"beat.adjust": handler.New(s.adjustBeat),

		"event.post": handler.New(s.postEvent),

		"listen.subscribe": handler.New(s.listenSubscribe),
		"listen.next":      handler.New(s.listenNext),
		"listen.close":     handler.New(s.listenClose),

		"index.span": handler.New(s.indexSpan),
		"index.read": handler.New(s.indexRead),
	}
}

// postEvent injects the given event into the feed.
func (s *service) postEvent(ctx context.Context, evt eventReq) (err error) {
	defer func() {
		if err == nil {
			jrpc2.ServerFromContext(ctx).Metrics().Count(metricEventsPosted, 1)
		}
	}()
	label := "event/user"
	if evt.Kind != "" {
		label += "/" + evt.Kind
	}
	return s.feed.Add(ctx, feed.Item{
		Label: label,
		Data:  evt.event,
	})
}

// listenSubscribe subscribes a new listener and returns its ID to the client.
func (s *service) listenSubscribe(ctx context.Context, opts listenOpts) interface{} {
	s.subs.Lock()
	defer s.subs.Unlock()

	id := uuid.New().String()
	lst := s.hub.Listen(&hub.ListenOptions{
		MaxQueueLen: opts.MaxQueueLen,
		MatchLabel:  match.AnyOrGlob(opts.Label),
		MatchData:   opts.Filter.compile(),
	})
	s.subs.m[id] = lst
	m := jrpc2.ServerFromContext(ctx).Metrics()
	m.EditLabel(metricHubListeners, func(v interface{}) interface{} {
		v.(map[string]*listenerInfo)[id] = &listenerInfo{
			Label:    opts.Label,
			Filtered: opts.Filter != nil,
			Max:      opts.MaxQueueLen,
		}
		return v
	})
	return handler.Obj{"id": id}
}

// listenNext retrieves the next item from the given listener.
func (s *service) listenNext(ctx context.Context, req listenReq) (*nextItem, error) {
	s.subs.Lock()
	lst := s.subs.m[req.ID]
	s.subs.Unlock()

	if lst == nil {
		return nil, fmt.Errorf("unknown listener ID %q", req.ID)
	}
	itm, err := lst.Next(ctx)
	if err != nil {
		if err == hub.ErrListenerClosed {
			s.subs.Lock()
			delete(s.subs.m, req.ID)
			m := jrpc2.ServerFromContext(ctx).Metrics()
			m.EditLabel(metricHubListeners, func(v interface{}) interface{} {
				delete(v.(map[string]*listenerInfo), req.ID)
				return v
			})
			s.subs.Unlock()
		}
		return nil, err
	}
	return &nextItem{
		entry: entry{
			Cursor: []byte(itm.Cursor),
			Label:  itm.Label,
			Data:   itm.Data,
		},
		Lost: lst.Lost(),
	}, nil
}

// listenClose closes and releases a listneer.
func (s *service) listenClose(ctx context.Context, req listenReq) error {
	s.subs.Lock()
	defer s.subs.Unlock()

	lst := s.subs.m[req.ID]
	if lst == nil {
		return fmt.Errorf("unknown listener ID %q", req.ID)
	}
	m := jrpc2.ServerFromContext(ctx).Metrics()
	m.EditLabel(metricHubListeners, func(v interface{}) interface{} {
		v.(map[string]*listenerInfo)[req.ID].Closed = true
		return v
	})
	return lst.Close()
}

// indexSpan reports the available index range.
func (s *service) indexSpan(ctx context.Context) (*span, error) {
	oldest, newest, err := s.index.Span(ctx)
	if err != nil {
		return nil, err
	}
	return &span{
		Oldest: []byte(oldest),
		Newest: []byte(newest),
	}, nil
}

// indexRead reads the contents of the index.
func (s *service) indexRead(ctx context.Context, req readQuery) (readResult, error) {
	const maxPerPage = 32
	if req.Count <= 0 || req.Count > maxPerPage {
		req.Count = maxPerPage
	}
	var res readResult
	err := s.index.Read(ctx, index.ReadQuery{
		Start:      string(req.Start),
		Limit:      string(req.Limit),
		MatchLabel: match.AnyOrGlob(req.Label),
		MatchData:  req.Filter.compile(),
	}, func(e index.Entry) error {
		if len(res.Entries) == req.Count {
			res.NextCursor = []byte(e.Cursor)
			return index.ErrStopReading
		}
		res.Entries = append(res.Entries, &entry{
			Cursor: []byte(e.Cursor),
			Label:  e.Label,
			Data:   e.Data,
		})
		return nil
	})
	if err != nil {
		return readResult{}, err
	}
	return res, nil
}

// adjustBeat adjusts the heartbeat rate.
func (s *service) adjustBeat(ctx context.Context, req *jrpc2.Request) error {
	const minInterval = 250 * time.Millisecond

	if s.hbeat == nil {
		return errors.New("system has no heartbeat")
	}

	var arg string
	if err := req.UnmarshalParams(&handler.Obj{"duration": &arg}); err != nil {
		return err
	} else if arg == "" {
		return &jrpc2.Error{Code: code.InvalidParams, Message: "no duration specified"}
	}
	dur, err := time.ParseDuration(arg)
	if err != nil {
		return fmt.Errorf("invalid heartbeat interval: %v", err)
	}
	if dur < minInterval {
		dur = minInterval
	}
	log.Printf("Resetting heartbeat duration to %v", dur)
	s.hbeat.Reset(dur)
	jrpc2.ServerFromContext(ctx).Metrics().SetLabel(metricBeatInterval, dur.String())
	return nil
}

// heartbeat periodically injects items into the feed, to simulate a workload
// from a running system.
func (s *service) heartbeat(ctx context.Context, d time.Duration) {
	start := time.Now().UTC()
	s.hbeat = time.NewTicker(d)
	defer s.hbeat.Stop()
	var nBeats int
	for {
		select {
		case <-ctx.Done():
			return
		case when := <-s.hbeat.C:
			err := s.feed.Add(ctx, feed.Item{
				Label: "event/internal",
				Data: event{
					Type: "heartbeat",
					Attrs: map[string]string{
						"beat":    strconv.Itoa(nBeats + 1),
						"time":    when.In(time.UTC).String(),
						"elapsed": time.Since(start).Truncate(10 * time.Millisecond).String(),
					},
				},
			})
			if err != nil {
				log.Printf("Heartbeat failed: %v", err)
				return
			}
			nBeats++
			log.Printf("[heartbeat] %d published", nBeats)
		}
	}
}

type event struct {
	Type  string            `json:"type"`
	Attrs map[string]string `json:"attributes,omitempty"`
}

func (event) DisallowUnknownFields() {}

type eventReq struct {
	event
	Kind string `json:"kind,omitempty"`
}

func (eventReq) DisallowUnknownFields() {}

type eventFilter struct {
	Type  string            `json:"type"`
	Attrs map[string]string `json:"attributes"`
}

func (eventFilter) DisallowUnknownFields() {}

func (e *eventFilter) compile() func(interface{}) bool {
	if e == nil || e.Type == "" && len(e.Attrs) == 0 {
		return func(interface{}) bool { return true }
	}
	mtype := match.AnyOrGlob(e.Type)
	mattr := make(map[string]match.Func)
	for k, v := range e.Attrs {
		mattr[k] = match.AnyOrGlob(v)
	}
	return func(data interface{}) bool {
		evt, ok := data.(event)
		if !ok || !mtype(evt.Type) {
			return false
		}
		for key, match := range mattr {
			val, ok := evt.Attrs[key]
			if !ok || !match(val) {
				return false
			}
		}
		return true
	}
}

type span struct {
	Oldest []byte `json:"oldest"`
	Newest []byte `json:"newest"`
}

type readQuery struct {
	Start  []byte       `json:"start"`
	Limit  []byte       `json:"limit"`
	Label  string       `json:"label"`
	Filter *eventFilter `json:"filter"`
	Count  int          `json:"count"`
}

func (readQuery) DisallowUnknownFields() {}

type readResult struct {
	Entries    []*entry `json:"entries"`
	NextCursor []byte   `json:"nextCursor,omitempty"`
}

type entry struct {
	Cursor []byte      `json:"cursor"`
	Label  string      `json:"label,omitempty"`
	Data   interface{} `json:"data"`
}

type listenReq struct {
	ID string `json:"id"`
}

func (listenReq) DisallowUnknownFields() {}

type listenOpts struct {
	MaxQueueLen int          `json:"maxQueueLen"`
	Label       string       `json:"label"`
	Filter      *eventFilter `json:"filter"`
}

func (listenOpts) DisallowUnknownFields() {}

type nextItem struct {
	entry
	Lost int `json:"lost,omitempty"`
}

type listenerInfo struct {
	Label    string `json:"label,omitemptY"`
	Filtered bool   `json:"filtered"`
	Max      int    `json:"maxQueueLen"`
	Closed   bool   `json:"closed,omitempty"`
}
