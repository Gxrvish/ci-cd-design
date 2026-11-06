# Milestone plan

Status: accepted 2026-11-06 (Gate B part 3).

- Budget: 6 hours a week (progress.md).
- The hours below are your hours: reading, YOU BUILD IT code, labs, break-it sessions, explain-back. My work happens inside the same sessions.
- The estimates are `[INFERRED]` from spec 19 and the decisions so far. Each milestone is re-estimated when it starts.
- Each milestone's scope, YOU BUILD IT, internals lab and break-it session are in spec 19. They are not repeated here.

## Plan

Dates assume a start on Monday 2026-11-09. Weeks are rounded up, which leaves about 4 weeks of buffer in total.

| Milestone | Hours | Weeks | Dates | YOU BUILD IT | Done when |
|---|---|---|---|---|---|
| M0 rest: Gate B part 3, Gate C, `unshare` lab | 10 | 2 | Nov 9 – Nov 22 | none | Gate C approved; lab notes in `docs/learning-log.md` |
| M1 walking skeleton | 21 | 4 | Nov 23 – Dec 20 | runner claim loop | `make demo` takes the fixture Dockerfile app from nothing to a working URL |
| M2 reliable job system | 27 | 5 | Dec 21 – Jan 24 | state machines, lease and fencing | `make chaos`: the 10 scenarios pass |
| M3 Git providers and webhooks | 16 | 3 | Jan 25 – Feb 14 | idempotency and supersede | a push to Gitea makes exactly one build; GitHub works through the tunnel |
| M4 identity, projects, dashboard | 33 | 6 | Feb 15 – Mar 28 | log chunk ingestion and resume | tenant isolation suite passes |
| M5 build engine and strategies | 27 | 5 | Mar 29 – May 2 | detection engine and one strategy | all fixtures build and run |
| M6 deployment engine | 22 | 4 | May 3 – May 30 | reconciliation loop | zero failed requests during a redeploy under load; rollback works |
| M7 previews and Runbot features | 21 | 4 | May 31 – Jun 27 | preview garbage collector | every eligible PR gets an isolated preview that is removed on close |
| M8 production hardening | 27 | 5 | Jun 28 – Aug 1 | none in spec 19 | full security suite passes; threat model reviewed; CloudForge deploys itself |
| M9 distributed and stretch | 21 | 4 | Aug 2 – Aug 29 | none in spec 19 | workloads run on the host and the VM |
| **Total** | **225** | **42** | **about 10 months** | | |

- **Core platform:** M1 to M6 make a working platform (build, deploy without downtime, logs, dashboard), done around the end of May 2027.
- **If time runs short:**
  - M8 can drop Let's Encrypt (it needs a real domain anyway).
  - M9 can stay a design document plus the VM runner.
- **Correction:** an earlier estimate said about 7 months. Estimating each milestone gives about 10. The difference is mostly M4 (the dashboard) and M8 (the security suite).

## A typical 6-hour week

1. **Session 1, about 2 h:** read what I prepared (the plan for the slice, the interface, the acceptance tests), then ask questions.
2. **Session 2, about 2 h:** write the YOU BUILD IT piece against the tests. HINT MODE is available.
3. **Session 3, about 2 h:** the internals lab or the break-it session, then answer the explain-back questions.

Weeks without a YOU BUILD IT piece use session 2 for reviewing my code in REVIEW MODE.

## Biggest schedule risks

1. **The dashboard in M4** (React, TypeScript, TanStack Query) is outside your usual stack. Time goes into reviewing it, not writing it.
2. **Chaos tests in M2** take time to make reliable: timing-based tests flake until the fake runner controls time exactly.
3. **Rootless BuildKit and the iptables rules** (M1, M8) depend on host details. The M1 lab tries them early, so surprises show up in week 4, not week 30.
