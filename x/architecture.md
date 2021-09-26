# Prototype: Event Routing for Tendermint

## Context

The Tendermint event system has been implicated in a number of performance and
reliability issues. We've been discussing ways to improve it, leading to [RFC
005: Event System][rfc005]. See also the discussion threads on [#6957][rfcdisc].
This prototype was motivated by those discussions and the related issues.

[rfc005]: https://github.com/tendermint/tendermint/blob/master/docs/rfc/rfc-005-event-system.rst
[rfcdisc]: https://github.com/tendermint/tendermint/pull/6957

### Example issues implicating event performance

- [Event publishing overwhelms websocket transport][i6729]
- [High memory usage with large numbers of event subscriptions][i6439]
- [Websocket stalls can halt the consensus node][i6184]
- [Proposed event system redesign][i4592]

Besides these specific issues, we have fairly good evidence that high event
processing load can slow down or possibly even deadlock consensus processing
due to synchronization pushback, and many requests for various kinds of event
indexing improvements motivated by the difficulty for clients to reliably keep
up with the event stream in real time, or catch up when it fails.

[i6729]: https://github.com/tendermint/tendermint/issues/6729
[i6439]: https://github.com/tendermint/tendermint/issues/6439
[i6184]: https://github.com/tendermint/tendermint/issues/6184
[i4592]: https://github.com/tendermint/tendermint/issues/4592

## Overview

The packages in this directory outline an incomplete, in-progress prototype of
a possible new architecture for internal event routing for Tendermint. The new
structure is intentionally similar to the existing implementation, in that
there is a common event bus shared by all components of the node that wish to
publish events, and a configurable hook for indexing.

The main differences in this design are:

- Remove tight coupling between the event bus and the external event streaming
  service, so that event subscribers cannot stall or significantly slow down or
  stall the consensus node due to synchronization feedback.

- Separate event indexing from event subscription, and provide a clean API to
  resume an interrupted subscription or catch up on dropped events due to slow
  processing, using the indexer as a source of truth.

- Make buffering and queueing more explicit throughout, greatly reduce the use
  of goroutines to track unfinished requests and channels for synchronization.

- Provide clearly-defined rules for blocking, concurrency, timeouts, and object
  lifetimes.  Use contexts consistently to manage request and goroutine
  lifetimes instead of special-purpose channel plumbing wherever practical.

The prototype implementation is intentionally minimalist, focusing mainly on
the underlying plumbing details rather than the Tendermint specifics. My hope
is that many of the current APIs can be re-implemented over this new substrate
without breaking existing usage at first. To get the full benefit of the new
design we would probably want to make some breaking changes as well, but we
should do that in a deliberate and controlled way, to minimize disruption.

## Demonstration

There is a working demonstration program in the [x/demo](./demo) directory.
To build it, check out this branch and run:

```sh
go build ./x/demo
```

The `demo` program simulates a Potemkin "node" doing work and publishing events
to the feed and hub system. The demo does not know anything about Tendermint or
consensus. The program exposes an HTTP server that accepts JSON-RPC requests to
publish events to a feed, query the index, and subscribe to listeners.

### Example Queries

The following examples assume you have Go and [jq](https://stedolan.github.io/jq/)
installed.

```sh
# -- Terminal 1
./demo -listen :2112

# -- Terminal 2
go install github.com/creachadair/jrpc2/cmd/jcall@latest

# Check the server status and get metrics.
jcall http://localhost:2112/rpc rpc.serverInfo '' | jq

# Subscribe to a listener and capture the ID for subsequent queries.
SUBID="$(jcall http://localhost:2112/rpc listen.subscribe '{"maxQueueLen":10, "label":"event"}' | jq -r .id)"

# Post some events that the listener will care about.
jcall http://localhost:2112/rpc event.post '{"type": "complaint", "attributes":[ {"key":"affect", "value":"vociferous"} ]}'
jcall http://localhost:2112/rpc event.post '{"type": "scheme", "attributes":[ {"key":"variety", "value":"harebrained"} ]}'

# Fetch the next event from the listener.
jcall http://localhost:2112/rpc listen.next '{"id":"'$SUBID'"}' | jq

# Release the listener.
jcall http://localhost:2112/rpc listen.close '{"id":"'$SUBID'"}'

# Fetch two more events from the listener, and observe that the listener is closed.
jcall http://localhost:2112/rpc listen.next '{"id":"'$SUBID'"}' | jq
jcall http://localhost:2112/rpc listen.next '{"id":"'$SUBID'"}'
```

## Discussion

The structure of this new design is based on two cooperating layers:

1. The "inner" layer, called the **feed**, is a pure time-series with no
   filtering. Events can only be published to the feed, and any component of
   the node can publish. The feed has a configurable fixed buffer capacity and
   publishers block when that capacity is reached.

   Each item added to the feed has a string label and an optional data item.
   Labels are not interpreted by the feed, but can be used to categorize items
   from different parts of the system -- for example, "consensus/event" for an
   event from the consensus engine, or "internal/timeout" to log a timeout in
   some internal system.

   Package: [x/feed](./feed/feed.go)

2. The "outer" layer, called the **hub**, is responsible for consuming items
   from the feed, indexing them, and distributing them to other components via
   a registry of **listeners**.

   Indexing is the only operation that can push back on the feed. Indexing is
   optional and configurable.

   Any component may register a **listener** on the hub, but items are
   distributed to listeners without blocking.  Each listener has a configurable
   fixed-size queue of "unconsumed" items, those received from the hub but not
   yet processed.  If the listener's owner does not timely consume items from
   the listener, the oldest unconsumed items will be evicted (discarded) from
   the queue.

   A listener has a label filter, so that only items with matching labels will
   be delivered to its queue. This gives the listener more control over which
   data it needs to respond to. By default, a listener receives everything.

   - Hub: [x/hub](./hub/hub.go)
   - Indexer: [x/index](./index/index.go)
   - Store: [x/index/memstore](./index/memstore/memstore.go)

Both the feed and the hub are always present. If a node wishes to disable
indexing or event subscription services, it need only reconfigure the hub;
settings for event indexing.  Buffer and queue capacities, concurrency,
timeouts, and so on, need not be plumbed to the components that generate the
events. All they need is a handle to the feed, which can be shared.

Indexing is designed reuse whatever storage implementation we wind up choosing
for the rest of the node, and should not require its own custom storage engine.
The prototype defines a particular interface, but the capabilities of that
interface can be easily shimmed on almost any practically-usable storage layer.

### Reliable Event Streaming

With the existing event bus, a subscriber that is too slow to process published
events may be dropped from the service, or some events may be discarded.  When
either of these things occurs, the subscriber has no easy way to tell which
events were lost.

Assuming a subscriber knows they've missed some events, they might attempt to
recover history by querying old events from the index based on a timestamp.  To
do that, it needs figure out a timestamp that covers the missing data and tell
which events it has already seen. It would be very difficult for the user of
the service to have confidence they'd actually seen everything.

To mitigate this, the new architecture explicitly associates each event with a
unique "cursor", notionally a row ID or other unique feature assigned by the
indexer. Each indexed item delivered to a listener by the hub will have a
cursor attached, and the indexer can answer simple range queries based on those
cursors. Moreover, listeners keep track of when (and how many) items they evict
from the queue, so that the owner can tell when items were dropped.

Combining these two features allows us to build reliable event streaming:

- The event service can detect when events are lost, and notify the
  client. This is not in the protocol right now, but could be added easily.

- The event service can detect when clients are slow, without slowing down the
  rest of the system. Slow clients do not create extra memory pressure, and can
  be dropped based on simple latency vs. loss metrics.

- A client can resume a dropped stream by providing the cursor of the last item
  they received. The service uses this cursor to scan the index for old events,
  until the client catches up to the active stream (which the service can see
  via a listener on the hub).

  If the catch-up process fails (e.g., the client remains slow, or disconnects
  again, or the node crashes), this can be repeated as often as necessary.

Moreover, this architecture makes it relatively easy to build "push" type event
notifications (e.g., via a webhook), which may be less resource cost for the
node than maintaining an active RPC client for each interested client.  This
could also be proxied off the node into another nearby process.

Having a better story for resumption would also allow us to shed load in a more
principled way: Currently, event subscriptions can put more or less unlimited
memory pressure on anode, even if they are timely about consuming the results.
If it is relatively easy for clients to resume, the node can tell clients off
to conserve resources, content that they can pick up later without issues.

With reliable resumption, a node could also proxy events a dedicated message
broker service (e.g., RabbitMQ, Kafka), so that the node operator would not
need to worry about resource scalability for large numbers of clients.  Such a
proxy could re-use much of the same plumbing code as the node itself.  That
would reduce our need to design against scale while maintaining a predictable
resource footprint for the consensus node.


## Block Diagram

Many details are omitted.

```
                                  +-----------+       +------------+
                                  |           +-------> listener   |
                                  |           |       +------------+
+------------------------+ items  |           |            ...
|          feed          +-------->    hub    |       +------------+
+------------------------+        |           +-------> listener   |
 "inner" layer                    |           |       +------^-----+
 all components publish           |           |              |
 events as items to the feed      +-----+-----+              | next(items)
                                        |                    |
                                  items |                    |
                                  +-----v-----+       +------+-----+
                                  |           |       |            |
                                  |  indexer  <-------+ subscriber +-----> event
                                  |           |       |            |       stream
                                  +-----+-----+       +------------+
                                        |        scan(entries)
                                entries |
                                  +-----+-----+
                                  |           |        Event service:
                                  |   STORE   |        - Scan indexer for old events
                                  |           |        - Listen to hub for current events
                                  +-----------+        - Client saves cursors for replay
```
