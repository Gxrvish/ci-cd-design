# ADR-0011: GitHub integration through a GitHub App

Status: accepted 2026-11-06 (Gate B part 3).
Confirms the Gate A choice (progress.md, 2026-11-05: GitHub App, to verify in part 3).

## Context

- Spec 8:
  - A provider interface, with Gitea locally and GitHub for real.
  - Compare a GitHub App with an OAuth App.
  - Document a tunnel for local GitHub webhooks.
- Spec 16: credentials are job-scoped and short-lived.
- Spec 12: the preview URL is posted back to the PR.

## Evidence

- `[DOCS]` Differences between GitHub Apps and OAuth apps (https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/differences-between-github-apps-and-oauth-apps):
  - GitHub Apps use fine-grained permissions, can be limited to chosen repositories, use short-lived tokens, and can act without a user.
  - GitHub Apps have "built-in, centralized webhooks" for every repository they can access. OAuth apps configure webhooks repository by repository.
  - Installation tokens expire after 1 hour. OAuth app tokens are long-lived by default.
- `[DOCS]` The OAuth `repo` scope "Grants full access to public and private repositories including read and write access to code, commit statuses, ... and repository webhooks" (https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps).
- `[DOCS]` The manifest flow registers a preconfigured GitHub App by following a URL:
  - The manifest supplies the permissions, events and webhook URL.
  - GitHub generates the webhook secret and the private key.

  Source: https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest.
- `[DOCS]` A commit status has a `context` and an optional `target_url`, and pull requests show it (https://docs.github.com/en/rest/commits/statuses).
- `[SOURCE]` smee-client (probot/smee-client 7e90106, ISC):
  - It re-serializes the forwarded body with `JSON.stringify(data.body)` (`index.ts:81`).
  - It subscribes to the channel URL with a plain `EventSource` and no credential (`index.ts:218`).

## Options

1. A GitHub App.
2. An OAuth App.

## Decision

A GitHub App.

- **Permissions:**
  - Metadata: read (always on).
  - Contents: read (clone).
  - Pull requests: read (PR events and metadata).
  - Commit statuses: write.
- **Events:** `push` and `pull_request`.
- **Registration:** each CloudForge installation registers its own app through the manifest flow (M3). Nobody shares an app or its key.
- **Where the app's secrets live:**
  - The app's private key can mint tokens for every installation. It and the app's webhook secret are files under `/run/secrets/`, handled like the KEK (ADR-0007), and mounted into `api` and `worker` only.
  - `git_repository.webhook_secret_enc` is used for Gitea only.
- **Tokens:** for each job, the Worker creates an installation token for one repository with `contents: read` (ADR-0007).
- **Statuses:**
  - The Worker posts the `cloudforge/build` status.
  - For PRs, it also posts `cloudforge/preview`, with `target_url` set to the preview URL. That covers spec 12's "post the URL back to the PR" with the one write permission. A PR comment would need another write permission.
- **User login:** stays CloudForge's own (M4). A GitHub App can add "sign in with GitHub" later through user access tokens.

### Local tunnel (spec 8)

smee.io, as chosen at Gate A, with two rules:

1. **The channel URL is the only secret.** Anyone who has it receives every delivery, including repository names and commit messages. Use a new channel and keep its URL out of the repository.
2. **Never turn signature verification off.**
   - smee-client re-serializes the JSON, so the bytes that reach the API can differ from the bytes GitHub signed, and the HMAC check can fail `[INFERRED]`.
   - Even then, verification stays on. The M3 break-it session checks whether the check fails.
   - Fallback: a tunnel that forwards raw HTTP, for example a Cloudflare quick tunnel, exposing only the webhook route.

## Why

- **OAuth App:** it acts as a user, its token is long-lived, and its `repo` scope reaches every repository that user can access, with write access. Losing it costs far more than losing a 1-hour token for one repository.
- **Central webhooks:** the app receives every installed repository's events at one URL. There are no per-repository webhooks or secrets to manage on GitHub.
- **Installation tokens** match spec 16's job-scoped credentials directly.

## Consequences

- **One private key with access to every installed repository.** It sits next to the KEK and gets the same care. To rotate it:
  1. Generate a new key in the app settings on GitHub.
  2. Deploy the new key.
  3. Delete the old one.
- **One URL for every repository's deliveries:** the Worker resolves the repository from the payload. This is already designed in the data model (`webhook_delivery.repository_id` is nullable).
- **Rate limits:** the limits for GitHub Apps scale with installations (docs above). They don't matter at our scale.
