# Authenticated local review

This harness is for isolated testing, not a production deployment. Tunnel addresses
and temporary user services are environment-specific; credentials stay in private
`.runtime` files and must never enter Git.

## Routes and authentication

- `/hub/` proxies to the Go Hub on loopback port 18081. Hub owns its PostgreSQL
  users, sessions, CSRF checks, account grants, and scoped API tokens.
- `/hub/mcp` accepts account tokens, not browser sessions. The configured
  `RELAY_PUBLIC_ORIGIN` host and loopback hosts are allowed; other hosts and browser
  Origin headers are rejected.
- With `RELAY_REVIEW_CORE_UPSTREAM=http://127.0.0.1:18085` and
  `RELAY_REVIEW_CORE_SITE=relay-ai-test.local`, `/relay` and Frappe routes go to the
  isolated installed Core. `/core/` redirects to `/relay`.
- Core uses the site's normal Frappe login, permissions, encrypted Password fields,
  and CSRF protection. Hub credentials and Frappe credentials are separate.

The gateway pins the Core site and HTTPS forwarding headers. Requests cannot use
an inbound site header to select another site. The installed workspace is built
with Core's `npm run build:site`; the standalone preview is not used for this path.
Without an installed Core upstream, the old authenticated static preview remains
available for design-only use.

## Services

The current isolated review uses user units `relay-review`, `relay-review-tunnel`,
`relay-core-review`, `relay-core-review-worker`, `relay-core-review-socket`, and
`relay-core-review-redis`.
Inspect their status with `systemctl --user status UNIT`. These transient units do
not guarantee availability after reboot. Never stop the shared PostgreSQL service
or an unrelated Bench to restart this review.

`RELAY_REVIEW_CORE_SOCKET` optionally points to the isolated Frappe realtime Unix
socket. The gateway forwards `/socket.io/` there with the same pinned site and
origin; no additional public TCP listener is required.

The Core worker projects signed callback events. The dedicated review account has
all provider channels disabled until real credentials are deliberately configured.
AI execution is also off. This environment contains synthetic simulation records.

## Verified behavior

Public HTTPS checks cover both logins, Hub account editing and saving, credential
redaction, blank-secret preservation, fresh account grants, Core-to-Hub account
binding, encrypted Core settings, signed callbacks, duplicate callback replay, and
forged-signature rejection. The real Core worker processed synthetic events into
Uncategorized because provider channels were intentionally disabled. Hub account
forms were checked at widths 320, 390, 768, and 1440 without horizontal overflow.

Provider simulation tests do not establish live Meta/Telegram/email acceptance,
provider eligibility, or complete production readiness. See [testing](testing.md)
and [provider support](provider-support.md).
