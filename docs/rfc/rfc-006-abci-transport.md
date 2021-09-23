# RFC 006: Node-Application Communication in ABCI++

## Changelog

- 23-Sep-2021: Initial draft (@creachadair).

## Abstract

> A brief high-level synopsis of the topic of discussion for this RFC, ideally
> just a few sentences.  This should help the reader quickly decide whether the
> rest of the discussion is relevant to their interest.

## Background

> Any context or orientation needed for a reader to understand and participate
> in the substance of the Discussion. If necessary, this section may include
> links to other documentation or sources rather than restating existing
> material, but should provide enough detail that the reader can tell what they
> need to read to be up-to-date.

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

  * FinishTask delivers a client response to a server task, providing the
    original taskID and the response. This method does not block beyond
    delivery.
