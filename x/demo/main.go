// Program demo is a working demonstration harness for the feed/hub
// prototype. Running the program starts a JSON-RPC service over HTTP that
// allows the caller to post events, query the index, and subscribe and consume
// items from the hub via listeners.
//
// Usage:
//    demo -listen [host]:port
//
//    # Server endpoint is: http://[host]:port/rpc
//
// To simulate an active node, the server runs a "heartbeat" that periodically
// injects new items into the feed. Set the period with the -heartbeat flag.
//
// RPC API:
//
//    Method           Parameters/Results
//    event.post       {kind: string, type: string, attributes: {key: value, ...}}
//
//    listen.subscribe {maxQueueLen: int}
//                     returns {id: string}
//
//    listen.next      {id: string}
//                     returns @entry: {cursor: bytes, label: string, data: any, lost: int}
//
//    listen.close     {id: string}
//
//    index.span       returns {oldest: bytes, newest: bytes}
//
//    index.read       {start: bytes, limit: bytes, label: string, count: int, filter: @filter?}
//                     where @filter: {type: string, attributes: {key: value, ...}}
//                     returns {entries: [@entry], nextCursor: bytes}
//
//    beat.adjust      {interval: duration}
//
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/jhttp"
	"github.com/creachadair/jrpc2/metrics"

	"github.com/tendermint/tendermint/x/feed"
	"github.com/tendermint/tendermint/x/hub"
	"github.com/tendermint/tendermint/x/index"
	"github.com/tendermint/tendermint/x/index/memstore"
)

var (
	listenAddr   = flag.String("listen", "", "Service address")
	beatInterval = flag.Duration("heartbeat", 5*time.Second, "Heartbeat intervao (0 to disable)")
	doDebug      = flag.Bool("debug", false, "Enable server debug logging")
)

const feedCapacity = 32

func main() {
	flag.Parse()

	if *listenAddr == "" {
		log.Fatalf("You must specify a non-empty -listen address")
	}
	log.Printf("Service URL: http://%s/rpc", *listenAddr)

	var logger *log.Logger
	if *doDebug {
		logger = log.New(os.Stderr, "[server] ", log.LstdFlags)
		log.Print("Enabled server debug logging (-debug)")
	}

	// Set up an in-memory index store.
	store := memstore.New()

	// Set up a feed and a hub.
	mainFeed := feed.New(feedCapacity, nil)
	mainHub := hub.New(mainFeed, &hub.Options{
		IndexItem: index.NewWriter(store).Write,
	})

	// Set up a context that will shut everything down.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up the service with access to the feed, the hub, and a query view of
	// the index. See the method definitions in service.go.
	svc := initService(&service{
		feed:  mainFeed,
		hub:   mainHub,
		index: index.NewReader(store),
	})

	// Start up the hub.
	go mainHub.Run(ctx)
	defer func() {
		log.Printf("Feed closed: %v", mainFeed.Close())
		log.Printf("Hub exited:  %v", mainHub.Wait())
	}()

	serverMetrics := metrics.New()
	serverMetrics.SetLabel(metricHubListeners, make(map[string]*listenerInfo))

	// Start up the heartbeat process (if enabled).
	if *beatInterval > 0 {
		go svc.heartbeat(ctx, *beatInterval)
		serverMetrics.SetLabel(metricBeatInterval, (*beatInterval).String())
		log.Printf("Enabling heartbeat with %v interval", *beatInterval)
	}

	// Register HTTP handlers.
	hs := &http.Server{Addr: *listenAddr, Handler: http.DefaultServeMux}
	bridge := jhttp.NewBridge(svc.methods(), &jhttp.BridgeOptions{
		Server: &jrpc2.ServerOptions{
			AllowV1:   true, // tolerate missing "jsonrpc" tags
			Logger:    logger,
			StartTime: time.Now(),
			Metrics:   serverMetrics,
		},
	})
	http.Handle("/rpc", bridge)

	// Catch SIGINT to shut everything down cleanly.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		defer signal.Stop(ch)
		select {
		case <-ctx.Done():
			return
		case <-ch:
			log.Printf("Interrupt received, shutting down")
			cancel()
			hs.Shutdown(context.Background())
		}
	}()

	log.Printf("Stopping server: %v", hs.ListenAndServe())
}
