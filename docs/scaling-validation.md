# Local validation gate — not production-ready

The Hub has provider adapters, PostgreSQL durable queues, bounded database pools,
per-account admission controls and adaptive WhatsApp workers. These are process-local
capacity controls; they do not supply machine/container autoscaling. Local fixture
regressions verify worker concurrency, replay and recovery, but do not establish
production throughput or end-to-end latency under sustained load.

## Required measured scaling behavior

Core submits bounded batches with idempotency keys. Hub acknowledges only after
persisting the batch. Stateless ingress and workers scale independently. A durable
queue separates provider dispatch from inbound reply delivery and status batching.

Worker demand is calculated from measured service time, runnable backlog and oldest
job age, with minimum/maximum workers and a global concurrency budget. A queue
blocked by provider throttling must not cause unbounded worker growth. Apply
per-account limits across all workers, retry backoff and fair scheduling.

On rising backlog, add capacity gradually within CPU, memory, provider and database
limits. On sustained low demand, stop claiming new work on selected workers, finish
or relinquish leased jobs, then stop them. Cooldown prevents repeated up/down
oscillation. Retain an always-available inbound callback path. Process autoscaling
requires a deployment controller; goroutines alone do not add server capacity.

Status delivery uses bounded batches and a maximum flush delay. Incoming replies
use a separate priority path so a large bulk send cannot block conversations.
Delivery is at-least-once; deduplication protects local state. Provider timeout after
acceptance can leave an ambiguous outcome: do not claim exactly-once external sends.

## Gates before another push

1. Installed isolated Core → real Hub API → fake provider → callback → Core/UI.
2. Permissions and tenant isolation, including direct calls bypassing the UI.
3. Step load at 50 and 100 messages/second, plus a 2,000-message burst; measure
   queue age, reply latency, throughput, failures, RSS, CPU and database connections.
4. Duplicate callbacks, rate limits, provider timeouts, Core/DB outages and worker
   termination. Confirm recovery, status reconciliation and bounded memory.
5. Increase load and observe actual scale-up; remove load and verify safe drain
   and scale-down. Verify hard concurrency limits and no rapid oscillation.
6. Run a sustained test and inspect query plans on realistic synthetic volume.
   Component virtualization tests do not establish end-to-end system capacity.

No production provider messages or production-site writes are needed for these gates.

## Read-only baseline, 2026-09-21

60 local /readyz requests at each concurrency: 1, 5, 20. All returned 200.
Observed p95: 4.33ms, 12.28ms, 35.25ms. This is a short readiness smoke test,
not a message throughput, sustained capacity, or autoscaling result.

## Measured local burst (24 September)

`TestOutboxDurabilityIsolationAndConcurrentWorkers` with `RELAY_TEST_BURST=2000`
passed against the isolated PostgreSQL database and local HTTP provider fixture
under the race detector. The burst admitted/drained in61.629seconds (about32.5
messages/second), peaked at2workers and returned to1idle worker. Final sampled
Go heap allocation was1,938,416bytes with13goroutines. These are end-of-phase
samples, not peak RSS or CPU measurements. The test also verifies account isolation,
uncertain-outcome handling and completed-intent replay without another send.

Evidence: `.runtime/burst-2000-tests.log`. The100-message default remains in the
normal regression suite. This does not satisfy sustained50/100messages-per-second
end-to-end targets or establish real-provider throughput; the larger capacity gates
above remain open.
