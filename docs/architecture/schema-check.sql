-- Checks the database rules in control-plane/migrations/0001_initial.sql. Status: accepted 2026-11-06 (Gate C part 1).
-- Run as cloudforge_app on a fresh database, after that file:
--   psql -v ON_ERROR_STOP=1 -f schema-check.sql
-- Each check prints one "ok" line. Anything unexpected stops the script with an error.
-- At M1 these checks move into the control plane's integration tests.

\set ON_ERROR_STOP on
\set QUIET on
\pset tuples_only on
\pset format unaligned

-- expect(label, sqlstate, statement): the statement must fail with exactly this error code.
-- The ok line also shows the constraint (or the message) that fired, so a check can't pass on the wrong rule.
CREATE FUNCTION pg_temp.expect(label text, code text, stmt text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    fired text;
    msg text;
BEGIN
    EXECUTE stmt;
    RAISE EXCEPTION 'expected % but it succeeded: %', code, label;
EXCEPTION WHEN OTHERS THEN
    IF SQLSTATE <> code THEN
        RAISE;
    END IF;
    GET STACKED DIAGNOSTICS fired = CONSTRAINT_NAME, msg = MESSAGE_TEXT;
    RETURN format('ok  %s  %s  [%s]', code, label, coalesce(nullif(fired, ''), left(msg, 60)));
END
$$;

-- Fixture: organizations acme and evil, one project with a production and a preview environment.
INSERT INTO organization (slug, name) VALUES ('acme', 'Acme') RETURNING id AS acme \gset
INSERT INTO organization (slug, name) VALUES ('evil', 'Evil') RETURNING id AS evil \gset
INSERT INTO git_repository (organization_id, provider, external_id, full_name, installation_id)
    VALUES (:acme, 'github', '1001', 'acme/shop', 7) RETURNING id AS acme_repo \gset
INSERT INTO git_repository (organization_id, provider, external_id, full_name, installation_id)
    VALUES (:evil, 'github', '2002', 'evil/tool', 8) RETURNING id AS evil_repo \gset
INSERT INTO project (organization_id, repository_id, slug, name)
    VALUES (:acme, :acme_repo, 'shop', 'Shop') RETURNING id AS shop \gset
INSERT INTO project (organization_id, repository_id, slug, name)
    VALUES (:acme, :acme_repo, 'blog', 'Blog') RETURNING id AS blog \gset
INSERT INTO branch (repository_id, name) VALUES (:acme_repo, 'main') RETURNING id AS main \gset
INSERT INTO pull_request (repository_id, number, head_sha, head_ref, base_ref, is_fork, state)
    VALUES (:acme_repo, 7, repeat('b', 40), 'feature', 'main', true, 'open') RETURNING id AS pr7 \gset
INSERT INTO pull_request (repository_id, number, head_sha, head_ref, base_ref, is_fork, state)
    VALUES (:acme_repo, 8, repeat('b', 40), 'fix', 'main', false, 'open') RETURNING id AS pr8 \gset
INSERT INTO app_user (email, password_hash) VALUES ('owner@acme.test', 'x') RETURNING id AS owner \gset
INSERT INTO environment (project_id, kind, branch_id) VALUES (:shop, 'production', :main) RETURNING id AS prod \gset
INSERT INTO environment (project_id, kind, pull_request_id) VALUES (:shop, 'preview', :pr7) RETURNING id AS preview \gset
INSERT INTO node (name, credential_hash) VALUES ('node-1', sha256('node-1')) RETURNING id AS node \gset

\echo '-- tenancy and inputs'
SELECT pg_temp.expect('a project cannot use another organization''s repository', '23503', format(
    $q$INSERT INTO project (organization_id, repository_id, slug, name) VALUES (%s, %s, 'steal', 'Steal')$q$,
    :acme, :evil_repo));
SELECT pg_temp.expect('a slug cannot look like a preview hostname', '23514', format(
    $q$INSERT INTO project (organization_id, repository_id, slug, name) VALUES (%s, %s, 'pr-12-x', 'X')$q$,
    :acme, :acme_repo));
SELECT pg_temp.expect('every runner belongs to an organization', '23502',
    $q$INSERT INTO runner (pool, name, state, credential_hash) VALUES ('trusted', 'r1', 'active', sha256('r1'))$q$);
SELECT pg_temp.expect('a runner token names its organization and pool', '23514',
    $q$INSERT INTO registration_token (kind, token_hash, expires_at) VALUES ('runner', sha256('t'), now() + interval '1 hour')$q$);
INSERT INTO webhook_delivery (provider, delivery_id, event, payload) VALUES ('github', 'd-1', 'push', '{}');
SELECT pg_temp.expect('a webhook delivery is stored once', '23505',
    $q$INSERT INTO webhook_delivery (provider, delivery_id, event, payload) VALUES ('github', 'd-1', 'push', '{}')$q$);
SELECT pg_temp.expect('approval sets sha, user and time together', '23514', format(
    $q$UPDATE pull_request SET approved_sha = head_sha WHERE id = %s$q$, :pr7));
SELECT pg_temp.expect('only fork PRs carry an approval', '23514', format(
    $q$UPDATE pull_request SET approved_sha = head_sha, approved_by = %s, approved_at = now() WHERE id = %s$q$,
    :owner, :pr8));
SELECT pg_temp.expect('a webhook secret is all five envelope columns or none', '23514', format(
    $q$INSERT INTO git_repository (organization_id, provider, external_id, full_name, webhook_secret_kek_id)
       VALUES (%s, 'gitea', 'g-2', 'acme/half', 'k1')$q$, :acme));
SELECT pg_temp.expect('a nonce is 12 bytes', '23514', format(
    $q$INSERT INTO environment_variable (project_id, name, scope, phase, ciphertext, nonce, wrapped_dek, dek_nonce, kek_id)
       VALUES (%s, 'API_KEY', 'all', 'build', '\x%s', '\x00', '\x%s', '\x%s', 'k1')$q$,
    :shop, repeat('00', 32), repeat('00', 48), repeat('00', 12)));

\echo '-- builds'
INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
    VALUES (:shop, :acme_repo, :prod, repeat('a', 40), 'webhook', true, 'queued') RETURNING id AS b1 \gset
SELECT pg_temp.expect('one webhook build per environment and SHA', '23505', format(
    $q$INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
       VALUES (%s, %s, %s, repeat('a', 40), 'webhook', true, 'queued')$q$, :shop, :acme_repo, :prod));
SELECT pg_temp.expect('a webhook never creates an untrusted build', '23514', format(
    $q$INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
       VALUES (%s, %s, %s, repeat('c', 40), 'webhook', false, 'queued')$q$, :shop, :acme_repo, :preview));
SELECT pg_temp.expect('a build cannot use another project''s environment', '23503', format(
    $q$INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
       VALUES (%s, %s, %s, repeat('c', 40), 'manual', true, 'queued')$q$, :blog, :acme_repo, :prod));
SELECT pg_temp.expect('a build is created queued', '23514', format(
    $q$INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
       VALUES (%s, %s, %s, repeat('c', 40), 'manual', true, 'running')$q$, :shop, :acme_repo, :prod));
SELECT pg_temp.expect('queued -> succeeded skips running', '23514', format(
    $q$UPDATE build SET state = 'succeeded', image_digest = 'sha256:' || repeat('e', 64) WHERE id = %s$q$, :b1));
UPDATE build SET state = 'running' WHERE id = :b1;
UPDATE build SET state = 'succeeded', image_digest = 'sha256:' || repeat('e', 64) WHERE id = :b1;
SELECT pg_temp.expect('succeeded is final', '23514', format(
    $q$UPDATE build SET state = 'failed', image_digest = NULL WHERE id = %s$q$, :b1));
INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
    VALUES (:shop, :acme_repo, :prod, repeat('1', 40), 'manual', true, 'queued') RETURNING id AS b2 \gset
UPDATE build SET state = 'running' WHERE id = :b2;
UPDATE build SET state = 'failed', reason = 'step_failed:test' WHERE id = :b2;

\echo '-- jobs'
INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
    VALUES (:shop, :acme_repo, :prod, repeat('2', 40), 'manual', true, 'queued') RETURNING id AS b3 \gset
INSERT INTO job (build_id, state) VALUES (:b3, 'queued') RETURNING id AS j3 \gset
SELECT pg_temp.expect('one job per build', '23505', format(
    $q$INSERT INTO job (build_id, state) VALUES (%s, 'queued')$q$, :b3));
SELECT pg_temp.expect('a running job holds runner, token, lease and deadline', '23514', format(
    $q$UPDATE job SET state = 'running', attempt = 1 WHERE id = %s$q$, :j3));
SELECT pg_temp.expect('attempt never exceeds max_attempts', '23514', format(
    $q$UPDATE job SET attempt = 4 WHERE id = %s$q$, :j3));

\echo '-- deployments'
SELECT pg_temp.expect('a deployment needs a succeeded build', '23503', format(
    $q$INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
       VALUES (%s, %s, 'sha256:' || repeat('e', 64), %s, 'pending', 'auto')$q$, :prod, :b2, :node));
SELECT pg_temp.expect('a deployment needs the build''s exact digest', '23503', format(
    $q$INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
       VALUES (%s, %s, 'sha256:' || repeat('9', 64), %s, 'pending', 'auto')$q$, :prod, :b1, :node));
SELECT pg_temp.expect('a build deploys only to its own environment', '23503', format(
    $q$INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
       VALUES (%s, %s, 'sha256:' || repeat('e', 64), %s, 'pending', 'auto')$q$, :preview, :b1, :node));
INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
    VALUES (:prod, :b1, 'sha256:' || repeat('e', 64), :node, 'pending', 'auto') RETURNING id AS d1 \gset
SELECT pg_temp.expect('one pending deployment per environment', '23505', format(
    $q$INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
       VALUES (%s, %s, 'sha256:' || repeat('e', 64), %s, 'pending', 'redeploy')$q$, :prod, :b1, :node));
UPDATE deployment SET state = 'live' WHERE id = :d1;
INSERT INTO deployment (environment_id, build_id, image_digest, node_id, state, trigger)
    VALUES (:prod, :b1, 'sha256:' || repeat('e', 64), :node, 'pending', 'redeploy') RETURNING id AS d2 \gset
SELECT pg_temp.expect('one live deployment per environment', '23505', format(
    $q$UPDATE deployment SET state = 'live' WHERE id = %s$q$, :d2));
BEGIN;  -- D2 done right: the old deployment leaves live first, in the same transaction
UPDATE deployment SET state = 'draining' WHERE id = :d1;
UPDATE deployment SET state = 'live' WHERE id = :d2;
COMMIT;
SELECT pg_temp.expect('live -> pending is not a transition', '23514', format(
    $q$UPDATE deployment SET state = 'pending' WHERE id = %s$q$, :d2));
SELECT pg_temp.expect('a deployed build''s digest cannot be swapped', '23503', format(
    $q$UPDATE build SET image_digest = 'sha256:' || repeat('8', 64) WHERE id = %s$q$, :b1));

\echo '-- history and privileges'
INSERT INTO build_event (build_id, from_state, to_state, reason, actor) VALUES (:b1, 'new', 'queued', 'created', 'worker');
SELECT pg_temp.expect('an actor has a known format', '23514', format(
    $q$INSERT INTO build_event (build_id, from_state, to_state, reason, actor) VALUES (%s, 'x', 'y', 'z', 'mallory')$q$, :b1));
SELECT pg_temp.expect('events cannot be edited', '42501', $q$UPDATE build_event SET reason = 'edited'$q$);
SELECT pg_temp.expect('events cannot be deleted', '42501', $q$DELETE FROM build_event$q$);
SELECT pg_temp.expect('the app cannot add transitions', '42501',
    $q$INSERT INTO state_transition VALUES ('build', 'succeeded', 'queued')$q$);
SELECT pg_temp.expect('the app cannot switch the trigger off', '42501',
    $q$ALTER TABLE build DISABLE TRIGGER build_state$q$);

\echo 'all checks passed'
