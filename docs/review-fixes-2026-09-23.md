# Review configuration fixes — 23 September 2026

The isolated review now runs the installed Core workspace and current Hub code.
Both repositories remain on `develop`. No production site or live provider
messages were used for these checks.

## Fixed

- Hub account editing handles Vue reactive configuration correctly. Account forms
  expose WhatsApp, Telegram, SMTP and Frappe callback configuration, channel
  activation, and saved-secret indicators. HTTP development site origins work.
- Private provider/callback fields are encrypted, redacted on reads, and preserved
  on blank edits. Migration 0018 and the transactional legacy-field upgrade retain
  existing credentials under the existing key. A database backup preceded review
  migration. Fresh setup generates independent encryption/webhook secrets.
- Newly created accounts, including Telegram/email-only accounts, are available
  for user grants immediately. `account.get` returns only the authenticated token's
  account/site binding, requires `account:read`, and rejects unavailable accounts.
- Public MCP requests work through the configured HTTPS hostname. Unknown hosts
  and browser-Origin requests remain rejected.
- Core `/relay` uses Frappe login and real backend APIs. System Manager connection
  settings verify Hub account/site binding and use encrypted Password fields.
  Desk configuration links and the isolated realtime socket work. The separate
  sample design preview is not the installed workspace.

The isolated fixture required synchronizing its framework schema and completing
its Frappe setup. This affected only `relay_ai_test_20260922`. The shared Frappe
checkout and other sites were not modified.

## Evidence

The final combined simulation report is `.runtime/simulation-report.json`:

| Check | Result |
| --- | --- |
| Hub Go race suite | 433 passing tests/subtests; no skipped integration tests |
| Go static analysis | Passed |
| Core unit contracts | 65 passed; 2 opt-in live-Hub smoke tests skipped |
| Core real Frappe/PostgreSQL scenarios | 100 passed across automation, channels, groups, typed messages and settings |
| Frontend data suites | Hub and Core passed |
| Frontend builds | Hub build and Core installed-workspace build passed |
| Additional isolated proxy checks | HTTP/site-header pinning and Unix realtime routing passed with race detector |

Public HTTPS browser and HTTP checks also passed:

- Hub login, account edit/save/reopen, runtime reload and blank-secret preservation.
- Core login, encrypted connection save through the public Hub MCP endpoint,
  Desk navigation, Secure/HttpOnly session cookie, CSRF rejection and logout.
- Signed callback acceptance, identical retry acceptance, forged signature
  rejection, and real worker processing to Uncategorized for disabled channels.
- Hub and Core settings at widths 320, 390, 768 and 1440 without horizontal overflow.

## User acceptance

The `relay-review` account and corresponding Core channel records are prepared
with WhatsApp, Telegram and Email disabled. Configure provider credentials in Hub,
activate the intended channel, and enable its matching Core channel. Configure
business Contact types and verified endpoints in Core before sending replies.

Provider eligibility, real deliveries/receipts, and Meta call/group/template
acceptance still require real accounts. Broader product limits are documented in
Core's `docs/implementation-status.md`; these fixes are not a claim that every
planned team, inbox, flow, email or automation feature is finished.
