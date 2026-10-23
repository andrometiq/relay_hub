# Hub v1 UI review

Changes are local to the Hub worktree on `feat/v1-ui`, uncommitted. No dependencies,
provider requests, production writes, migrations, commits or pushes were performed.
The shared `frontend/node_modules` symlink was used only for existing tooling.

## Screens

- Overview: database readiness, runtime account count, aggregate queue counts,
  per-account credential/configuration gaps, webhook and Core receipt timestamps,
  tracked callback pending/failure/quarantine counts. The account list is paginated;
  Overview describes the currently loaded account page. Delivery totals explicitly
  cover all existing Hub transports because the aggregate API is not channel-scoped.
- Accounts: one shared searchable, paginated account list; identity, encrypted
  credential replacement, template/metadata capabilities, ordered webhook setup,
  Core credentials, preserved legacy channels/features and retryable activation.
- Connections: WhatsApp display number, verified name, quality, verification time,
  metadata freshness and independently observed webhook/Core delivery evidence.
- Deliveries: account-scoped sends, loaded-page counts, attempts, provider errors,
  queued/submitted/delivery/read distinctions, evidence-based uncertain resolution,
  confirmation before cancellation, callback failures, replay and quarantine.
- Events: account scope, search across loaded records, explicit older-page loading,
  forwarding status and payload inspection capped at 65,536 characters. System
  activity is a tab containing audit outcomes, actor and correlation IDs.
- Users/access: explicit and effective account grants, role explanations, user
  enable/disable/reset, scoped keys with expiration/revocation, Sessions and password.

Navigation uses a persistent desktop sidebar and mobile drawer at <=640px. Status
and notifications remain visible in both layouts. Lists become cards on mobile;
mobile virtual-list clipping is removed. Refresh occurs on mount, explicit actions
and mutations; there is no polling or new realtime transport. Notifications report
latest request health, not a durable backend notification feed.

## Review corrections

Both reported defects were confirmed and fixed:

- User enable/disable now rereads the selected user's access through the existing
  cursor API, replacing the dialog's complete user record. Effective grants are
  cleared while refreshing. If the read fails after saving, the dialog reports that
  distinction beside the action and shows unknown access instead of stale grants.
- Mobile pagination anchors a surviving visible record using measured DOM positions
  before and after the update, with native scroll anchoring disabled to prevent
  double correction. Measurement happens when the response arrives, so scrolling
  during a pending request is respected. The 200-record bound remains in place;
  fixed-height desktop virtualization retains its existing offset calculation.

## Read-only Go additions

- `GET /api/accounts/health?account=...`: administrator-only, aggregate observed
  webhook timestamps and callback receipts. No provider calls or queue changes.
- `/auth/users` reads additionally return explicit `grants` and
  `effective_grants` (enabled user and account). Existing mutations are untouched.

## Validation

- `node --test frontend/tests/*.test.mjs`: 15 tests pass, including preservation of
  hidden channel configuration, reactive account editing, create/activation failure
  recovery, CSRF propagation and bounded/redacted server diagnostics.
- `npm run build --prefix frontend`: passes. `build:site` is Core-only and not run.
- `go test ./cmd/relay-hub ./internal/auth`: passes. Database-dependent tests skip
  without `RELAY_TEST_DATABASE_URL`; this is not database integration validation.
- `frontend/tests/browser/v1.mjs`: built-asset Chrome checks at 320, 375, 640, 768 and
  1440px; all main routes, account refresh/save/retry, configuration preservation,
  callback failure/success, cancellation refresh, event payloads, no horizontal
  overflow, no polling, no browser errors.
- `frontend/tests/browser/access.mjs`: built-asset Chrome checks of 350 users with
  variable-height mobile cards at 375/640px and virtual desktop rows at 1440px.
  Six forward pages and three backward pages verify record trimming at the
  200-record bound and preserve a visible record's position within one pixel,
  including scrolling during delayed responses. Disable/re-enable refreshes grants;
  failed mutations and failed grant refreshes show inline errors without stale
  access claims. Expired keys, Sessions navigation, dark theme and reduced motion
  also pass with local API fixtures.
- `git diff --check`: passes.

Browser scripts use an existing Playwright installation via `PLAYWRIGHT_MODULE`;
no package installation is necessary. Start Vite preview from `frontend` on port
18129. Keep temporary browser files inside `.runtime/tmp` using `TMPDIR`.
The fixtures use clearly named local-only records, non-zero queues and failure
states; they do not call Meta or a Frappe site. They are not full release validation.

## Remaining gaps

1. No isolated Hub database, `.env`, local runtime or test DSN is configured in this
   worktree. Real Go + PostgreSQL + provider/Core mock integration and sustained
   load/failure validation remain outstanding. No migration was attempted under the
   task's approval restriction. Do not treat this change as release-ready yet.
2. `messages_per_second` is an allowed stored account property, but
   `runtimeWhatsApp` does not copy it into the runtime account. The UI shows the
   configured value read-only and explains that runtime defaults apply. Fixing its
   behavior requires a backend change outside the permitted read-only additions.
3. Runtime reports only an aggregate WhatsApp account count, not per-account loaded
   identity. The UI labels account enablement as configuration and separately shows
   observed webhook/Core evidence; it does not equate these with end-to-end readiness.
4. Event search is limited to loaded records; there is no server-side full-history
   search. Callback backlog counts cover tracked receipts, not unscheduled events.
   Metadata sync has no administrator trigger in the existing browser API.
5. Notifications summarize request failures and last-check time. No backend durable
   notification/acknowledgement feed or socket exists. Explicit Refresh remains the
   supported update mechanism.
