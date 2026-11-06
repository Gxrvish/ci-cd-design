# ADR-0015: Dashboard sessions, CSRF and a separate site for the dashboard

Status: accepted 2026-11-06 (Gate C part 2c).

## Context

- Spec 15 and M4: login, organizations and roles, a React dashboard on the same API.
- Threat model #5 and #6: stolen sessions, cross-site requests. Gate C was to choose the CSRF defense.
- Spec 16 says every repository is hostile, and the platform deploys their apps on `*.cloudforge.localhost`, previews from fork PRs included (ADR-0010, ADR-0013).

## Evidence

- `[DOCS]` Public Suffix List: if no rule matches a domain, the prevailing rule is `*` (https://github.com/publicsuffix/list/wiki/Format). Browsers use the list to group cookies and history by site (https://publicsuffix.org/learn/).
- `[RAN]` `localhost` does not appear in `public_suffix_list.dat` (16,584 lines, fetched 2026-11-06).
  - So the site of `pr-12-x.cloudforge.localhost` is `cloudforge.localhost`.
  - A dashboard at `cloudforge.localhost` would share its site with every deployed app `[INFERRED]`.
- `[DOCS]` OWASP CSRF cheat sheet (https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html):
  - `Sec-Fetch-Site` is the primary signal; a fallback to `Origin` verification is mandatory.
  - All major browsers have sent these headers since March 2023. No client changes are needed.
  - Don't set cookies for a whole domain: every subdomain then shares them.
- `[DOCS]` OWASP password storage: Argon2id, or, if that is not available, scrypt with N = 2^17, r = 8, p = 1 at minimum (https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
- `[DOCS]` Python's standard library has `hashlib.scrypt`. `maxmem` defaults to OpenSSL's 32 MiB limit (https://docs.python.org/3/library/hashlib.html).
- `[DOCS]` FastAPI 0.132.0 rejects JSON requests whose `Content-Type` isn't JSON, by default. A plain HTML form can't post JSON to us (https://fastapi.tiangolo.com/release-notes/).

## Options

- **CSRF defense:**
  1. A synchronizer token stored per session.
  2. A signed double-submit cookie.
  3. Fetch Metadata plus an `Origin` check.
- **Dashboard site:**
  - A. `cloudforge.localhost`, the same site as the apps.
  - B. A site of its own.

## Decision

Option 3 and option B.

1. **Origin:** `http://cloudforge-console.localhost` serves the dashboard's static files, `/api` and `/webhooks`, through Nginx. One origin, so no CORS. Apps stay on `*.cloudforge.localhost`, which is a different site.
2. **Session:**
   - The `cf_session` cookie holds 32 random bytes. It is `HttpOnly`, `SameSite=Strict` and `Path=/`, with no `Domain`, so it goes to this host only.
   - With HTTPS (M8) it also gets `Secure` and the `__Host-` prefix.
   - The server stores the hash (`user_session`). Sessions last 7 days, without sliding. Logout and a password change revoke them.
3. **CSRF:** any request other than GET or HEAD that carries the session cookie needs `Sec-Fetch-Site: same-origin`. If that header is missing, `Origin` must equal the dashboard origin. Otherwise the answer is 403.
4. **API tokens:** sent as `Authorization: Bearer <token>`. A token acts as its user and expires within 1 year. No cookie is involved, so there is no CSRF check.
5. **Passwords:**
   - Hashed with `hashlib.scrypt` (N = 2^17, r = 8, p = 1) and a 16-byte random salt, with `maxmem` raised to 256 MiB.
   - Stored as `scrypt$N$r$p$salt$hash`, so the parameters can be raised later and a login rehashes with the new ones.
   - The first registered user becomes the platform admin `[OUR DECISION]`.

## Why

- **Option B:** with option A, hostile code at `pr-12-x.cloudforge.localhost` is same-site with the dashboard:
  - the browser attaches even `SameSite=Strict` cookies to its requests;
  - it can set cookies for `cloudforge.localhost` (cookie tossing) `[INFERRED]`.

  With B, the browser treats every deployed app like any other website.
- **Option 3:** no token storage or client code. OWASP calls it simpler to get right than tokens. We require `same-origin`, stricter than OWASP's default of blocking only `cross-site`, because nothing else should ever write to the API from a browser.
- **Server-side sessions:** logout and "revoke everything" are one row update. Signed stateless tokens would need key handling and can't be revoked.
- **scrypt:** it is in the standard library, so there is no new dependency for Argon2id. The cost is about 128 MiB of memory during each login check.

## Consequences

- **Amends (applied 2026-11-06):**
  - ADR-0010: the dashboard host is `cloudforge-console.localhost`, also covered by the M8 certificate.
  - Threat model: #6 uses this defense, plus a new threat: a hostile app attacking the dashboard.
- **Public installs (M8):**
  - Apps need their own registrable domain, separate from the dashboard's.
  - On one shared domain, the `Origin` check is the only barrier, and cookie tossing stays possible.
- **Still open:** no MFA (threat #5); login throttling is an Nginx `limit_req` rule in M8.
- **M1 to M3 have no login.** This stub goes into `docs/stubs.md`.
