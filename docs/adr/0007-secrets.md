# ADR-0007: Secrets: envelope encryption and job-scoped delivery

Status: accepted 2026-11-06 (Gate B part 2). Amended 2026-11-06 by part 3: shared runners dropped (ADR-0009); fork approval never unlocks variables (ADR-0013).
Not a YOU BUILD IT piece (I implement it in M4), but it is on the explain-back list (spec 22: how a fork PR can steal secrets).

## Context

The spec sets these requirements:
- Spec 6: no plaintext secrets; envelope encryption; document key handling and rotation.
- Spec 8: fork PRs get no secrets.
- Spec 12: a preview never reads production secrets.
- Spec 16: job-scoped credentials; a compromised runner must not read other projects' secrets; masking is best effort, with its limits documented.

## Evidence

- `[DOCS]` Envelope encryption: a data encryption key (DEK) encrypts the data and is itself wrapped by a key encryption key (KEK). The guidance:
  - Store the DEK next to the data.
  - Generate a new DEK every time the data is written, so DEKs never need rotating.
  - Keep KEKs central, and rotate them regularly and after a suspected incident.

  Source: https://cloud.google.com/kms/docs/envelope-encryption.
- `[DOCS]` AES-GCM in the Python `cryptography` package: 12-byte nonce, "NEVER REUSE A NONCE with a key". Associated data is authenticated but not encrypted (https://cryptography.io/en/latest/hazmat/primitives/aead/).
- `[SOURCE]` GitLab encrypts CI variable values with AES-256-GCM, a random IV per value and one application key (`db_key_base_32`). That is direct encryption, not envelope encryption (gitlab `app/models/concerns/ci/new_has_variable.rb:11-15`, MIT).
- `[DOCS]` Docker: build arguments and environment variables persist in the final image. Pass build secrets as secret mounts instead: `docker build --secret id=...,env=...` and `RUN --mount=type=secret,id=...,env=...` (https://docs.docker.com/build/building/secrets/).
- `[DOCS]` Compose: environment variables are visible to all processes and can end up in logs. Secrets are mounted as files at `/run/secrets/<name>` (https://docs.docker.com/compose/how-tos/use-secrets/).
- `[RAN]` `docker inspect --format '{{json .Config.Env}}'` on a container started with `-e CF_DEMO_SECRET=s3cr3t` printed `"CF_DEMO_SECRET=s3cr3t"`. Anyone with Docker access on a node can read runtime env vars.
- `[DOCS]` GitHub Actions: apart from `GITHUB_TOKEN`, secrets are not passed to the runner when a fork triggers the workflow (https://docs.github.com/en/actions/security-for-github-actions/security-guides/using-secrets-in-github-actions).
- `[DOCS]` GitLab: "Masking a CI/CD variable is not a guaranteed way to prevent malicious users from accessing variable values". Pipeline code can also send variables to a third-party server (https://docs.gitlab.com/ci/variables/).
- `[SOURCE]` GitLab Runner masks secrets even when a value is split across writes. It stacks one writer per value, longest first, and replaces each match with `[MASKED]` (gitlab-runner `common/buildlogger/internal/masker/masker.go:1-12`, MIT).
- `[DOCS]` GitHub App installation tokens expire after 1 hour and can be limited to listed repositories and permissions (https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

## Options

1. Direct encryption with one application key (GitLab's approach).
2. Envelope encryption: a fresh DEK per value, wrapped by a KEK kept outside the database.
3. An external secret manager (Vault, a cloud KMS) from day one.

## Decision

Option 2, with the KEK in a local file. If the KEK later moves to Vault Transit or a cloud KMS, the stored format stays the same: only the wrap and unwrap functions change.

## Why

- Option 1 ties every row to one key. Rotating it means re-encrypting everything, and the key has to sit in the app. The spec also asks for envelope encryption explicitly.
- Option 2 makes KEK rotation cheap: re-wrap the small DEKs, never touch the data. It is the same shape every KMS uses, so moving to option 3 later is a swap, not a migration.
- Option 3 adds a service to run before there is anything that needs that level of protection.

## Key handling

- **KEK**: 32 random bytes per key, each with an id (`k1`, `k2`, ...).
  - Stored as files in a keyring directory, mounted read-only into the `api` and `worker` containers only (Compose secrets, `/run/secrets/`).
  - Created by `make init`, mode 0400, git-ignored.
  - Never in the database, never in env vars, never logged. Config names the active id.
- **Write**:
  1. Generate a new 32-byte DEK.
  2. Encrypt the value with AES-256-GCM (DEK, random 12-byte nonce, associated data).
  3. Wrap the DEK with AES-256-GCM (active KEK, random nonce, same associated data).
  4. Store the ciphertext, both nonces, the wrapped DEK and `kek_id`.
- **Associated data** = `env:<project_id>:<scope>:<name>`. Someone with write access to the database cannot move project A's ciphertext into project B's row: decryption fails. Renaming a variable therefore re-encrypts it.
- **Read**: only these code paths decrypt:
  - building a claim response;
  - building a node's desired state;
  - the Worker's calls to the Git provider;
  - the webhook intake, for a repository's webhook secret.

  The dashboard never shows a value again after it is saved.
- **Backups**: database backups and KEK copies are stored apart. One without the other is useless. Losing every KEK copy loses every secret.
- **Hashed, not encrypted**: credentials the platform only verifies are stored as hashes. That covers runner and agent credentials, job tokens and registration tokens: SHA-256 of 32 random bytes. A fast hash is enough because the input can't be guessed. Passwords get a slow hash (M4).

## Rotation

- **DEK**: nothing to do. Every write creates a new one.
- **KEK** (yearly, or when the key file may have leaked):
  1. Generate `k2` and add it to the keyring.
  2. Make `k2` active. New writes use it.
  3. Run the re-wrap command. For every row with `kek_id = k1`, it unwraps the DEK with `k1` and wraps it with `k2`. Value ciphertexts don't change.
  4. Check that no row still references `k1`.
  5. Remove `k1` from the keyring, but keep a copy offline for as long as any database backup taken before step 3 exists. Those backups need it.
- **Leaked secret values** (a database dump and the KEK leaked together, or a value showed up somewhere): rotating the KEK doesn't help, because the attacker already has the plaintext. Rotate the values at their source (for example, issue new API keys), then save the new ones.

## Delivery

Each variable has a `scope` (an environment kind, or `all`) and a `phase` (`build`, `runtime`, or both).

- **Build phase**: a claim response carries only the build-phase variables of the claimed job's project that match the job's environment. Untrusted jobs get none.
  - The runner keeps them in memory and hands them to BuildKit as secrets. They are visible only to `RUN` steps that mount them and are never written into the image. They are never passed as build args.
  - Strategy-generated Dockerfiles mount them in the install, build and test steps. A user's own Dockerfile must add the mounts itself.
- **Runner scope**: every runner belongs to one organization and one pool (`trusted` or `untrusted`), and claims only that organization's jobs. This keeps spec 16 ("a compromised runner must not read other projects' secrets") true across organizations.
  - Shared runners (no organization) were dropped in part 3: a shared build cache lets one tenant poison another's (ADR-0009).
  - Untrusted builds (fork PRs) run only on the `untrusted` pool and get no variables (ADR-0013).
- **Runtime phase**: a node's desired state carries runtime variables only for deployments placed on that node, and only the agent credential can fetch it. The agent passes them as container env vars.
- **Previews**: preview-scope and all-scope variables only, never production-scope (spec 12).
- **Fork PRs**: `trusted = false`, so no user secrets in the build or the preview (spec 8). A maintainer's approval of a SHA does not unlock preview-scope variables either (ADR-0013).
- **Clone access**:
  - GitHub: an installation token limited to the one repository with `contents: read`, valid for at most 1 hour.
  - Gitea (dev only): public repositories, or a read-only deploy key per repository. That key is long-lived; this is a documented limit.

## Masking (best effort)

- The runner masks every build-phase value of 8 characters or more before upload, so plaintext never reaches the log tables or the archives.
- Shorter values are not masked, because they would match too much ordinary output. The dashboard warns when one is saved.
- The runner holds back the last (longest value − 1) bytes of output, so a value split across two reads or two chunks is still caught. GitLab Runner's stacked writers use the same idea.
- Limits, also stated in the dashboard:
  - A value printed in a transformed form (base64, hex, reversed, one character per line) is not masked.
  - Hostile code can send a value over the network instead of printing it (builds may reach the internet, ADR-0012).

  Masking catches accidents. It is not a security boundary.

## What a compromised component can read

| Compromised | Can read | Bound |
|---|---|---|
| A runner (for example after a container escape on its node) | build-phase secrets of the jobs it claims from then on | It never gets runtime secrets. Runner scope limits it to its own organization's jobs and pool. Revoking it (R4) ends access. Within that organization it can still collect other projects' build secrets over time; project-scoped runners would close that and are not planned. |
| An agent or node | runtime secrets of the deployments on its node, and anything else in Docker there | Docker access is root on that node anyway. |
| The API or Worker process | all secrets, because it holds the KEK | It never runs repository code (spec 5.1) and keeps few dependencies. |
| A database dump on its own | nothing in plaintext | The KEK lives elsewhere. |

## Consequences

- One new Python dependency, `cryptography`, because the standard library has no AES.
- Secret values are write-only in the dashboard.
- Claim responses carry secrets, so the runner API must use TLS as soon as it leaves the host (M9's VM).
- Runtime secrets also rest unencrypted on the node, in Docker's stored container config, readable by root `[INFERRED]` (see the `[RAN]` line above).
