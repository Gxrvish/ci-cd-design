# ADR-0013: Branch types and the fork PR rule

Status: accepted 2026-11-06 (Gate B part 3).
The preview garbage collector is YOU BUILD IT in M7.

## Context

- Spec 7: branch types (production, staging, dev, preview), inspired by Odoo.sh. Each has its own retention, resource limits, secrets and database behavior. Superseded builds are cancelled automatically.
- Spec 8: fork PRs get no secrets, and no preview until a maintainer approves.
- Spec 12: a preview has its own container, record, URL, variable scope and, from M7, database. It is removed on PR close or after a TTL, and it never acts as production.
- Open question from part 2: does approving a fork PR's SHA unlock preview-scope variables?

## Evidence

- `[DOCS]` Odoo.sh (Gate A, section 7):
  - Staging branches use neutralized duplicates of the production database.
  - Development branches create new databases with demo data to run tests, and can be garbage-collected.
- `[SOURCE]` Runbot (Gate A, sections 1.2 and 1.8):
  - Base bundles are sticky.
  - Each host keeps at most `running_max` running builds and kills older ones unless protected.
  - A per-build database is created from a template with `createdb -T`.
- `[DOCS]` Vercel: a PR from a fork needs a team member's authorization before it deploys (Gate A, section 5).
- `[DOCS]` Render: a preview copies the base service's environment variables, including database connection info (Gate A, section 6). The spec forbids that for us.
- `[DOCS]` GitHub Actions:
  - Workflow runs from fork PRs may need a maintainer's approval, and the maintainer is told to inspect the changes first.
  - Secrets other than `GITHUB_TOKEN` are not passed to workflows triggered from forks.

  Sources: https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-workflow-runs/approving-workflow-runs-from-public-forks, ADR-0007.
- `[DOCS]` GitLab:
  - A parent-project member can run a fork MR's pipeline in the parent project, with the parent's CI/CD variables.
  - The docs warn: "Fork merge requests can contain malicious code that tries to steal secrets in the parent project". The UI asks the member to accept a warning first.

  Source: https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/.

## Decision

### What each Git event produces

| Event | Environment kind | Trusted | Build | Deploy |
|---|---|---|---|---|
| push to the production branch | production | yes | yes | auto |
| push to a branch listed as staging in the project | staging | yes | yes | auto |
| push to any other branch | development | yes | yes, with tests | no |
| PR opened, synchronized or reopened, from the same repository | preview | yes | yes | auto |
| PR from a fork | preview | no | only after approval | only after approval |
| PR closed | preview | n/a | open builds superseded | environment closed (D4, D6) |

- "Trusted" means the commit came through the base repository, so whoever pushed it has write access there.
- Development branches build and test but don't deploy `[OUR DECISION]`. This host's RAM is the limit, and the PR preview covers review.

### Policy per kind

Starting values; M7 and M8 tune them.

| | production | staging | development | preview |
|---|---|---|---|---|
| variables (ADR-0007) | production + `all` | staging + `all` | development + `all` | preview + `all`; fork: none |
| app limits | 1 CPU, 512 MiB, 256 pids | 0.5 CPU, 384 MiB, 256 pids | none (no deploy) | 0.5 CPU, 256 MiB, 128 pids |
| images kept | last 5 succeeded (rollback) | last 2 | 7 days | until the environment closes |
| lifetime | permanent | permanent | n/a | closed on PR close, or after 7 days without a new commit |
| database (M7) | the user's own, through variables | the user's own | none | its own, cloned from the project's template |
| superseded builds cancelled | yes | yes | yes | yes |

### Deployment guard

A deployment's build must match its environment:

- same project;
- same target (`branch:<name>` or `pr:<n>`);
- trusted, for production and staging.

D1 (state-machines.md) checks this, and a test covers it. Together with the rules below, this is how spec 12 holds: a preview never routes as production, never reads its secrets and never reaches it.

- Per-environment networks and hostnames (ADR-0010).
- Variable scopes (ADR-0007).

### Fork PR rule

1. **A fork PR is always untrusted, whoever opened it.** The person who opens a PR is not necessarily the author of its commits, and a membership check would be one more path to get wrong.
2. **Nothing runs before approval.** A maintainer (role owner or admin) approves the PR's current head SHA in the dashboard. In one transaction, the API:
   - stores `approved_sha` and `approved_by`;
   - creates the build, with trigger `manual`.

   The update is a compare-and-set on the head SHA, so a double click creates one build.
3. **A new push needs a new approval.** It changes the head SHA, which invalidates the approval: each SHA needs its own.
4. **Approval never unlocks secrets.**
   - Approved fork builds run only on `untrusted` runners (ADR-0009), with no variables.
   - Their preview gets no variables either.
   - This answers the part 2 open question: no.
5. **A full preview needs the base repository.** A maintainer who wants one pushes the fork's commits to a branch of the base repository. That is a deliberate act by someone with write access, and the result is a trusted same-repository PR.

This rule is stricter than spec 8, which only requires approval before the preview `[OUR DECISION]`. Without it, unapproved hostile code would burn CPU on our runners (mining, attacks on the runner itself). GitHub's approval gate for fork workflows works the same way.

## Why

- **Approval is a review of one exact commit.** A secret must not depend on someone noticing something in a diff. GitLab shows the alternative: running fork code with the parent's variables is a documented way to steal secrets, guarded only by a warning dialog.
- **Fork previews can still have a working database (M7).** Values the platform generates, such as the preview database's password, are not user secrets.

## Consequences

- **Fork previews that need API keys don't work.** This is documented, together with the push-to-base-repository workaround.
- **Branch protection is part of the security model.** Anyone who can push to the production branch can read production build secrets through the build. Branch protection on the Git provider covers this, and the docs say so.
- **New project setting:** `staging_branches`, a list.
- **New role check:** approving a fork PR requires owner or admin.
- **No new tables:** `pull_request.approved_sha`, `pull_request.approved_by` and `build.trusted` already exist.
- **Superseded fork PR builds:** a new push to a fork PR cancels its approved build (supersede, ADR-0004). Nothing new starts until the next approval.
