# RFC 9177 Milestone 3 completion addendum

Authority: the approved RFC 9177 foundation and connection-pacing designs.
The user explicitly approved server-only public UDP construction in Milestone 3.
Public client probing/fallback modes and DTLS Q wiring remain Milestone 4.

Complete the remaining areas sequentially with bounded plans and inline TDD:
1. Combined Q1 upload/Q2 response packet-size negotiation.
2. Aggregate owned storage including transient copies, sparse receiver indexing,
   preparation, executor and handler/callback lifetimes, and retained metadata.
3. Endpoint congestion ownership shared across connections to the same peer,
   including detach/reconnect debt and scheduler notification without lock inversion.
4. Public `options.WithQBlockServer(qblock.ServerConfig)` applying only to UDP
   server configuration. Support initial GET and assembled POST/PUT through normal
   handlers; share endpoint ownership and shut down cleanly. Ordinary traffic and
   disabled-Q rejection retain their current behavior.
5. Runtime acceptance and one final whole-branch review. Compile-only and focused
   passes do not substitute for a complete runtime pass.

## Combined packet sizes

A private Q1 sender advertises a single QBlock2(NUM=0,M=1,SZX=response ceiling)
with each upload packet. Compute that ceiling from the receiving datagram budget
before admission, independently of upload offsets. Include its encoded overhead
when selecting upload SZX. The server accepts only this initial advertisement
alongside Q1, excludes it from upload identity, and requires it to remain stable.
Absent hints preserve existing response behavior. Response SZX is bounded by the
advertised ceiling and the server send budget, rather than the upload SZX.
A larger first Q2 response is ignored before Q1 completion or congestion feedback.
A changed Continue SZX terminates the exchange before advancing upload offsets;
resending partially delivered uploads at a new SZX is outside this slice.

## Ownership and congestion boundaries

Limits describe live application-owned payload/backing/index/bookkeeping storage,
not Go runtime RSS or garbage-collection implementation details. Admission must
precede owned copying. Handler/callback reservations survive cancellation until
that invocation returns. Explicit bounded storage replaces unaccountable sparse
map growth. Connection-local retained limits remain enforceable sublimits.
Endpoint congestion records normalize peers (including IPv6 zone), own their lock,
and retain unacknowledged debt across connection teardown. Feedback ownership
must prevent an old connection from settling newer work. Never invoke connection
callbacks or take connection locks while holding the endpoint lock.
