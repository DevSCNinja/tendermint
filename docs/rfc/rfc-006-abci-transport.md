# RFC 006: Node-Application Communication in ABCI++

## Changelog

- 23-Sep-2021: Initial draft (@creachadair).

## Abstract

As we prepare to make the changes to Tendermint to support the newer
[ABCI++][abci++] protocol, we should also try to resolve some of the related
issues around how the application communicates with Tendermint.

## Background

Up through Tendermint v0.35, applications communicate with the consensus node
via the [Application BlockChain Interface (ABCI)][abci]. Starting with v0.36,
we are migrating to an updated protocol called [ABCI++][abci++] that addresses
several important use cases for application authors.

Although ABCI++ shares a lot in common with ABCI, it also introduces new points
of communication with the consensus node. Since we would like to simplify and
rationalize the interprocess communication surfaces of Tendermint, this is a
good opportunity to resolve some outstanding design issues for node/application
communication.

These issues include:

- **Out-of-process transport:** Applications not written in Go communicate with
  the consensus node either via gRPC or using a simple custom protocol that
  exchanges protocol buffers over a Unix-domain or TCP socket.


Issues to link:
https://github.com/tendermint/tendermint/issues/6899 [global lock on local client]
https://github.com/tendermint/tendermint/issues/5439 [crashes in concurrent checks]
https://github.com/tendermint/tendermint/issues/5519 [order of execution]


## References

- [Application BlockChain Interface (ABCI)][abci]
- [ABCI++][abci++], a refinement of ABCI
- [RFC 002: Interprocess Communication in Tendermint][rfc002]

[rfc002]: https://github.com/tendermint/tendermint/blob/master/docs/rfc/rfc-002-ipc-ecosystem.md
[abci]: https://github.com/tendermint/spec/tree/95cf253b6df623066ff7cd4074a94e7a3f147c7a/spec/abci
[abci++]: https://github.com/tendermint/spec/blob/master/rfc/004-abci%2B%2B.md

> Links to external materials needed to follow the discussion may be added here.
>
> In addition, if the discussion in a request for comments leads to any design
> decisions, it may be helpful to add links to the ADR documents here after the
> discussion has settled.

## Discussion

> This section contains the core of the discussion.
>
> There is no fixed format for this section, but ideally changes to this
> section should be updated before merging to reflect any discussion that took
> place on the PR that made those changes.

- Cheap low-bandwidth server-to-client calls: Server maintains two new methods:

  * GetTask blocks indefinitely until the server has a request for the client
    or is about to terminate. It returns a request or an error. A request is
    bundled with a unique identifier (taskID).

    The client calls GetTask in a loop, as long as it has slots to handle more
    work. The server keeps a queue of pending work for the client and delivers
    the next element whenever the client calls, or blocks.

  * FinishTask delivers a client response to a server task, providing the
    original taskID and the response. This method does not block beyond
    delivery.
