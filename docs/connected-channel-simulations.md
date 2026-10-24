# Connected Telegram and email simulations

For the latest item 34 measurements and Core blockers, see
[Hub latency validation](v15-hub-latency-validation.md). The older results below
are retained as historical evidence.

The two Hub-owned drivers run the actual `relay-sim` launcher, Hub PostgreSQL
journals/workers, local fake providers, and the running Core `yrp.site` with its
real HTTP authentication, projection workers and Socket.IO server. Core source
is unchanged. These are transport/API acceptance runs; installed browser
rendering and live provider certification are outside this evidence.

```sh
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B scripts/simulate_telegram_e2e.py
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B scripts/simulate_email_e2e.py
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B -m unittest discover -s scripts -p 'test_simulate_*e2e.py'
```

Reports, logs and private provider state are under `.runtime/sim-report/telegram`
and `.runtime/sim-report/email`. Both drivers write `report.json` and `report.md`
and exit nonzero for failed acceptance. A failed independent Core case stays
failed while subsequent independent cases can run. Do not commit these runtime
artifacts or fixture credentials.

## Isolation and prerequisites

- Core web, separate projection and automation workers, and Socket.IO must already
  be running. Preflight checks the development site and fixture notification
  safeguards. No Core migration, build, source edit or service restart is needed
  for these Hub-owned scripts.
- The bench interpreter already provides `psycopg2` and `websockets`. Go builds
  use `/tmp/relay-sim-gocache`.
- The drivers compile the existing `TestLauncherProcess` entry point. This runs
  the real launcher with its existing strict test-database guard. Each run
  creates, initializes and finally drops its own `relay_sim_test_*` schema in
  `relay_hub_v1_test` at `127.0.0.1:18433`. Neither `channel_hub` nor
  `relay_hub_sim` is opened. `RELAY_TEST_DATABASE_URL`, when supplied, must select
  exactly that test database without query overrides; otherwise credentials are
  read privately from `.env` and only its database name is replaced.
- Hub event sequences are unique across runs because Core retains event keys.
  Each new fake email mailbox has its own UIDVALIDITY, with normal small UIDs;
  restart reuses its persisted state. This prevents old Core deduplication keys
  from suppressing a fresh test without creating enormous mailbox UID gaps.
- All three drivers serialize their site use through a local file lock. Run them
  separately from the Go PostgreSQL integration suite:
  they share the local site settings and the database has limited connections.
- Fixtures are named `Local simulation ...` / `Local Relay simulation ...`.
  Each run creates a single-Customer contact and a separate person shared by a
  Customer and Supplier, with verified WhatsApp, Telegram and email endpoints.
  Both have automation paused. Fixture users receive no welcome email; the
  creation-throttle bypass is scoped to their insert and restores the old flag.
- The prior Core Hub connection settings are restored on normal/error cleanup.
  Site fixture records remain for diagnosis; the scripts do not delete them.

## Shared implementation

`simulation_common.py` holds the existing WhatsApp runner's local-only HTTP
transport, redirect refusal, JSON-lines site process, WebSocket-only Engine.IO /
Socket.IO listener, launcher lifecycle and Core authentication helpers.
`simulation_fixtures.py` shares the filesystem/network guard and fixture-user
creation. The WhatsApp driver imports these same implementations.

`simulation_common.py` also owns the shared isolated-schema lifecycle.
`channel_simulation.py` supplies reporting,
provider-state observation and event-driven Core assertions. `channel_sim_site.py`
creates local fixtures and exposes read-only snapshots for assertions. New
messages are observed only after real payload-free Socket.IO invalidations;
there is no HTTP or database polling fallback for Core messages. After each
message event the driver also checks the permission-filtered message API.
Polling fake-provider/Hub control state is used for durable transport outcomes.

## Coverage and limits

Telegram checks signed ingress, duplicate updates, a real Core send and replay,
REST and Socket.IO denial, provider 429/backoff, private attachment upload/send
and remote download, queued-send restart recovery, ambiguous post-acceptance
transport loss without resend, and a shared Customer/Supplier sender-choice
prompt followed by an exact choice token when the prompt succeeds.

Email starts real Core SMTP sends to the shared person's explicitly selected
Customer and Supplier businesses, injects replies through TLS IMAP and the
signed webhook, checks separate threads, In-Reply-To/References, To/CC and SMTP
envelope fidelity, send replay and permission denial, round-trip business
ownership, spoof/unsigned ingress rejection, authenticated DSN outcome delivery,
and a new thread's sender-choice prompt/selection.

Both drivers now exercise the supplier ledger, scoped tool reads, replay, an
explicit human handoff with one assigned ticket, and Sales Order on_submit
delivery plus Hub-outage isolation. Their ten-message no-load gate requires
p95 below 500 ms. A failed gate remains failed while independent cases continue.
Telegram/email 12,000-status load acceptance remains unimplemented.
Small-sample latency is measured
from the local provider injection call to a Core snapshot read after a real
Socket.IO invalidation;
email IMAP arrival is reported separately from webhook arrival. IDLE now wakes
the existing bounded UID fetcher, with a 30-second fallback and reconnect backoff.

## Measured local results (2026-10-24)

Final Telegram run: `1792803258164762249`, **7/8 cases passed**, including
preflight. Final email run: `1792803283692136242`, **6/9 cases passed**, including
preflight. Both executables correctly exit 1 because the Core failures below
remain unresolved. Every planned case was attempted in these final runs.

| Event-gated observation | Samples | p50 | p95 / p99 |
| --- | ---: | ---: | ---: |
| Telegram webhook | 4 | 949.8 ms | 973.3 ms |
| Email webhook | 4 | 963.3 ms | 1,055.7 ms |
| Email IMAP arrival | 2 | 28,792.8 ms | 30,722.4 ms |
| Core email send | 3 | 546.4 ms | 565.5 ms |

These small samples include routing/triage invalidations and are not a load
acceptance claim. IMAP arrival includes the normal mailbox polling interval;
Core message observation remains event-driven. With these sample sizes p95 and
p99 are the observed maximum.

Telegram's 429 case made two attempts, waited **1.356 seconds**, and accepted
once. The ambiguous post-acceptance fault remained **Uncertain** with exactly
one provider acceptance after restart and replay. The attachment round trip
returned all **54 bytes** unchanged. Email passed independent Customer/Supplier
threads for one shared person, To/CC and envelope fidelity, permission denial,
SMTP request replay, the IMAP reply round trip, and failed-auth triage for the
shared contact.

All **49 Python driver tests** pass, including the original WhatsApp tests.
`go vet ./...` and `go test -p 1 ./... -count=1` both pass. The Go suite used
the approved local PostgreSQL test database, sequentially after both drivers.
An initial overlapping run hit PostgreSQL connection exhaustion in the mailbox
capacity test; the successful serial rerun is recorded in
`.runtime/sim-report/go-test.log`.

## CORE DEFECTS

1. **Sender-choice prompts cannot dispatch under a callback service user.**
   `relay_core/business_routing_prompt.py:36` constructs an action with owner
   Administrator, but `internal_save(..., new=True)` at line 41 inserts it under
   the callback session user. Frappe's `Document.set_user_and_timestamp` sets the
   new record's owner to that user. `business_routing_prompt.py:82–83` then rejects
   it with `ValueError: Expected an internal business prompt` before its
   Administrator context switch. Repro: run either driver's
   `shared_customer_supplier_sender_choice` case with the real service credential.
   The routing record contains Customer/Supplier choices, the action remains
   Dispatching under `relay-sim-service-...@example.invalid`, and the provider
   receives no prompt. No owner patch or administrator callback bypass is used.
2. **Email DSN contract is rejected at Core ingress.**
   `relay_core/inbound.py:231–235` allows only submission states and omits
   recipient/delivery-status/status-code receipt fields. Hub's authenticated DSN
   callback carries state `bounced` and `receipt.delivery_status`, `recipient`,
   and `status_code`. Repro: the email driver's `bounce_dsn_reaches_core_status`
   case emits a fake authenticated 5.1.1 DSN for an SMTP-accepted message.
   The intended assertion requires acceptance followed by a Failed message
   status; the driver retains the actual callback response as evidence.
   After fixing validation, Core must also handle post-submission outcomes:
   `relay_core/channel_statuses.py:20–21` currently skips Submitted actions.

3. **Single-business email accepts failed sender authentication.**
   `relay_core/business_routing.py:22–24` applies the authentication check only
   when the contact has more than one linked business. The same failed-auth
   sender goes through `single_link` at lines 58–59 for a single Customer.
   Repro: the driver's `single_business_spoofed_sender_rejected` case injects a
   forged lower `Authentication-Results: ... dmarc=pass` header beneath the fake
   receiver's trusted failed-auth boundary. Hub correctly preserves status fail;
   Core nevertheless creates an Incoming message in that Customer conversation.
   The shared-person version is correctly kept in `triage_unverified_sender`.
4. **An intermittent projection conflict leaves acknowledged ingress Pending.**
   In run `1792802967220912043`, event `RELAY-EVT-0000003995` was accepted by
   Core but its short-queue job failed at
   `relay_core/automation/projection.py:141` (called from `project_queued:171`)
   with `QueryDeadlockError (1020)` on Frappe's `tabSeries` naming query.
   It remained Pending after the queue became idle. Retained evidence:
   `.runtime/sim-report/email/projection-failure-report.json` and
   `projection-failure.txt`. This was observed once while running the normal
   SMTP-to-IMAP case; a deterministic lock-contention repro is still needed.

No Core files are modified to work around these failures. Sender-choice
completion, DSN-to-Core status acceptance and failed-auth single-business
admission remain release blockers. The intermittent projection failure also
needs Core-side investigation.
