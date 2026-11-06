# ADR-0004: Webhook ingestion and idempotency

Status: accepted 2026-11-05 (Gate B part 1). References to part 3 resolved 2026-11-06 (ADR-0013).
The idempotency and supersede logic is YOU BUILD IT in M3. This ADR fixes the rules.

## Context

Spec section 8: never build synchronously; verify, persist, enqueue, return quickly. Duplicate deliveries and redundant events must not create duplicate builds. Target: webhook acknowledged in under 500 ms at p95. M3 "break it": replay a webhook 50 times and send events out of order.

## Evidence

- `[DOCS]` GitHub: respond with a 2XX within 10 seconds or the delivery counts as failed; process asynchronously; `X-GitHub-Delivery` is a GUID per event and "will be the same as in the original delivery" on redelivery (https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks). `X-Hub-Signature-256` is `sha256=` + hex HMAC of the body (https://docs.github.com/en/webhooks/webhook-events-and-payloads).
- `[DOCS]` Gitea: `X-Gitea-Delivery` is a "unique delivery UUID for this attempt"; `X-Gitea-Signature` is a hex HMAC-SHA256 without prefix; Gitea also sends GitHub-style `X-Hub-Signature-256`; with no secret configured, the signature headers are present but empty (https://docs.gitea.com/usage/webhooks).
- `[SOURCE]` Runbot treats a webhook as a doorbell: it records the time and re-fetches refs from Git, ignoring the commits in the payload. It checks the signature only when a secret is configured (`TODO enforce`) (`runbot/controllers/hook.py:18-72`).

## Options

1. Trust the payload: build the SHA it names; dedupe on delivery id.
2. Doorbell: store the delivery, then ask the provider for the current head of the branch or PR and build that; dedupe on (project, target, SHA).
3. Polling only (Runbot's `poll` mode), no webhooks.

## Decision

Option 2. The delivery is still stored, for audit and replay.

## Rules

1. Intake (API, inside the request): read the raw body; verify `X-Hub-Signature-256` with a constant-time compare (both providers send this header, so one code path); reject a missing or empty signature (a secret is mandatory); insert the delivery with a unique key (provider, delivery id); on a duplicate, still answer 2xx. Return `202`. Nothing else happens in the request.
2. Processing (Worker): take unprocessed deliveries with row locks that skip locked rows. For `push` and `pull_request`: resolve the target (`branch:<name>` or `pr:<number>`), ask the provider for that target's current head SHA, ensure exactly one webhook-originated build exists for (project, target, SHA), supersede, mark the delivery processed.
3. Supersede: cancel queued and running builds of the same target with a different SHA (spec section 7). Other targets are untouched. Every branch type supersedes, production included (ADR-0013).
4. A closed PR triggers preview teardown (M7). Fork PRs: nothing builds until a maintainer approves the head SHA, and approval never unlocks secrets (ADR-0013, M7).

## Why

- Exact replays are caught by the delivery id, because GitHub keeps the id on redelivery. Everything else is caught by the (project, target, SHA) key, including a Gitea redelivery that carries a new UUID.
- Order stops mattering: by processing time, the payload's SHA may be older than the branch head. Re-reading the head turns an old event into "build the current head", which already exists, so nothing happens.
- Lost webhooks can be repaired later by a periodic loop that calls the same "ensure build for current head" path.

## Consequences

- One provider API call per delivery. Fine at our volume; rate limits would matter at scale.
- A push to a branch with an open PR yields two targets (branch and PR), so up to two builds. Every branch type builds; which ones deploy is ADR-0013.
- The intake is one INSERT, which keeps the 500 ms target easy.
