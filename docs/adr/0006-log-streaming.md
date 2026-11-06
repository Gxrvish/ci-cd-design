# ADR-0006: Log streaming and storage

Status: accepted 2026-11-06 (Gate B part 2). Amended 2026-11-06 by Gate C part 2c: the log stream is addressed by build id.
Log chunk ingestion with sequence numbers and resume is YOU BUILD IT in M4. This ADR fixes the rules, not the code.

## Context

The spec sets these requirements:
- Spec 5.2.6: ordered chunks with sequence numbers; hot logs in the database with a retention limit; archive to object storage when the build ends; the UI resumes from an offset.
- Spec 15: live logs over SSE or WebSocket, with resume.
- Spec 14 and 16: no unbounded logs in PostgreSQL; log size limits.
- M4 break-it: disconnect the browser mid-stream and resume without losing lines.

Gate A left one question open: byte offsets (as Runbot and GitLab use) or sequence numbers.

## Evidence

- `[SOURCE]` GitLab Runner appends with `PATCH /jobs/:id/trace` and a `Content-Range` byte range. On a mismatch the server answers `416` with its current size and the runner rewinds (gitlab-runner `network/gitlab.go:834-955`; gitlab `lib/api/ci/runner.rb:333-355`).
- `[SOURCE]` GitLab keeps live logs as 128 KiB chunks in Redis, then moves full chunks to object storage or the database (gitlab `app/models/ci/build_trace_chunk.rb:21-59`).
- `[SOURCE]` GitLab Runner caps a job's log and keeps the job running: "Job's log exceeded limit of %v bytes. Job execution will continue but no more output will be collected." (gitlab-runner `helpers/trace/buffer.go:188-196`).
- `[SOURCE]` Runbot reads each build's log file from a byte offset that it stores in the database (`runbot/models/host.py:295-330`).
- `[DOCS]` SSE: when an `EventSource` reconnects, the browser sends the last event id it received in the `Last-Event-ID` header (https://html.spec.whatwg.org/multipage/server-sent-events.html, section 9.2).
- `[DOCS]` PostgreSQL `text` cannot store the NUL character (https://www.postgresql.org/docs/current/datatype-character.html).
- `[DOCS]` `NOTIFY` is delivered only after the sending transaction commits. Payloads must stay under 8000 bytes, so send a key and read the row (https://www.postgresql.org/docs/current/sql-notify.html).
- `[SOURCE]` Odoo's bus works exactly this way. It inserts `bus.bus` rows and calls `pg_notify('imbus', ...)` in a post-commit hook. The listening side runs `LISTEN imbus`, then reads the rows (Odoo community `addons/bus/models/bus.py:135-165, 240-243`, LGPL-3, local checkout `a789a218`).
- `[DOCS]` Nginx buffers proxied responses unless the upstream sends `X-Accel-Buffering: no`. `proxy_read_timeout` defaults to 60 s (https://nginx.org/en/docs/http/ngx_http_proxy_module.html).

## Options

Ordering:
1. Byte offsets: each upload says where it starts, and the server appends only if that matches its current size.
2. Sequence numbers: chunk `n` of attempt `a`, keyed by (job, attempt, seq).

Live delivery to the browser:
- A. SSE; the handler polls the database every second.
- B. SSE; the handler sleeps until a `NOTIFY` wakes it.
- C. WebSocket.

## Decision

Option 2 with A. B is the upgrade path.

## Why

- Both orderings work. Sequence numbers turn a retried upload into a primary-key conflict, so idempotency needs no arithmetic. The sequence number also serves as the SSE event id, so the browser resumes for free through `Last-Event-ID`.
- Polling costs one indexed query per open stream per second: 20 open log views mean 20 cheap queries per second. B removes that, but adds a dedicated listening connection and fan-out code. Switch when log viewers show up in database load. It is the same pattern as Odoo's bus.
- SSE beats WebSocket here. The stream is one-way, it is plain HTTP, and the browser already reconnects and resumes. With WebSocket we would write both ourselves.

## Rules (the invariants the M4 tests check)

1. **Runner, before upload**:
   - Mask secrets (ADR-0007).
   - Replace invalid UTF-8 and NUL bytes with U+FFFD.
   - Cut chunks at a newline when possible, at most 64 KiB each.
   - Flush at least once per second while output flows.
2. **Runner upload**:
   - One chunk in flight per job, in order. `seq` starts at 0 for each attempt.
   - Retry the same chunk until it gets a 2xx, with backoff capped at 2 s.
   - Keep chunks in memory until acknowledged.
3. **API, on each chunk**: the call is fenced like every job call (ADR-0003 rule 3). Then it compares `seq` with the next expected number:
   - Equal: insert, answer 202.
   - Lower: a retry. Answer 202 and write nothing.
   - Higher: answer 416 with `expected_seq`. A gap can only come from a runner bug or a database restore. The runner continues from `expected_seq` and writes a `[log gap]` line.
4. **Limits**:
   - A chunk over 64 KiB gets 413.
   - Per attempt, the runner stops collecting at 10 MiB, writes one line saying so, and lets the build continue (GitLab's behavior).
   - As a backstop, the API rejects anything past 10 MiB + 64 KiB with 413.
5. **Reading** (`GET /api/builds/{id}/attempts/{n}/log`, as SSE; the dashboard never sees job ids):
   - Event id = `seq`. Data = JSON `{"seq": n, "text": "..."}`, because JSON escapes the `\r` and `\n` that SSE would otherwise treat as line breaks.
   - Resume after `Last-Event-ID`, or after `?after=` on the first connection.
   - Send a comment line every 15 s so proxies don't time out, and the header `X-Accel-Buffering: no` so Nginx doesn't buffer.
   - Send an `end` event after the last chunk of a finished attempt.
6. **Archive**: when an attempt ends, the Worker writes its chunks to object storage as one object (`logs/<org>/<project>/<build>/a<attempt>.log`). Then, in one transaction, it inserts an Artifact row (kind `log`, size, SHA-256) and deletes the hot chunks.
   - If it crashes between the two steps, the next run simply repeats the upload to the same key.
   - Hot chunks are deleted only after a successful archive, so nothing is lost. A metric counts attempts still unarchived after 1 h.
7. **Reading an archived attempt**: the reader gets the whole file through a presigned URL. There is nothing left to stream.

## Consequences

- PostgreSQL holds at most about 10 MiB per running attempt, usually for minutes.
- A log line reaches the browser in up to about 2 s (1 s runner flush + 1 s poll). The target is < 3 s (`docs/architecture/failures-and-targets.md`).
- Each attempt has its own log, so retries keep their history (spec 6).
- Before M4, logs use a simpler path with no object storage and no resume. This goes into `docs/stubs.md` when M1 starts.
- Per-step log views: the runner prefixes each line with its step, such as `[install]` (ADR-0008).
