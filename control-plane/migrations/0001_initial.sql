-- CloudForge schema, migration 0001
-- Status: accepted 2026-11-06 (Gate C part 1).
-- Target: PostgreSQL 18 (checked with 18.1, see docs/architecture/schema-check.sql).
-- Concepts and rules: docs/architecture/data-model.md, state-machines.md. Data access and migrations: ADR-0014.
--
-- Conventions [OUR DECISION]
-- * Table names are singular. USER is a reserved word in PostgreSQL, so the user table is app_user.
-- * Keys are bigint identity columns. Times are timestamptz, always database time (ADR-0003 rule 1).
-- * Closed value sets are text + CHECK, not enum types: changing a CHECK is one ALTER, and enum values can't be removed.
-- * Constraints that would span tables are composite foreign keys, so tenant and environment
--   mix-ups fail in the database, not only in code.
-- * No ON DELETE CASCADE. The only rows ever deleted are archived log chunks, expired sessions and old
--   webhook deliveries (garbage collection, M8). Deleting a project is not designed yet.
-- * Secrets: envelope columns (ADR-0007). Credentials: SHA-256 hashes (ADR-0007).
-- * Two database roles. The migration role owns every table; the app role cloudforge_app (created by
--   local setup, Gate C part 3) gets the grants at the end of this file.

-- Reusable value types --------------------------------------------------------------------------

CREATE DOMAIN git_sha AS text CHECK (VALUE ~ '^[0-9a-f]{40}([0-9a-f]{24})?$');  -- SHA-1 or SHA-256 repositories
CREATE DOMAIN image_digest AS text CHECK (VALUE ~ '^sha256:[0-9a-f]{64}$');
CREATE DOMAIN sha256_hash AS bytea CHECK (octet_length(VALUE) = 32);         -- hashed credentials and tokens
-- Who made a state change (state-machines.md rule 3). 'api' is for M1 only, before login exists (stub).
CREATE DOMAIN actor AS text CHECK (VALUE ~ '^((user|runner|agent):[1-9][0-9]*|worker|api)$');

-- 1. Identity and tenancy ------------------------------------------------------------------------

CREATE TABLE app_user (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email             text NOT NULL UNIQUE CHECK (email = lower(email) AND email LIKE '%_@_%'),
    password_hash     text NOT NULL,                  -- slow hash, chosen in M4
    is_platform_admin boolean NOT NULL DEFAULT false, -- may create node registration tokens
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- Browser sessions (M4). The cookie holds 32 random bytes; only their hash is stored.
CREATE TABLE user_session (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES app_user,
    token_hash sha256_hash NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX user_session_user ON user_session (user_id);

-- Personal API tokens for scripts and tests (M4). They act as their user.
CREATE TABLE api_token (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES app_user,
    name         text NOT NULL,
    token_hash   sha256_hash NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_token_user ON api_token (user_id);

CREATE TABLE organization (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$'),  -- image path part
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organization_member (
    organization_id bigint NOT NULL REFERENCES organization,
    user_id         bigint NOT NULL REFERENCES app_user,
    role            text NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE INDEX organization_member_user ON organization_member (user_id);

-- 2. Git ----------------------------------------------------------------------------------------

CREATE TABLE git_repository (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id bigint NOT NULL REFERENCES organization,
    provider        text NOT NULL CHECK (provider IN ('gitea', 'github')),
    external_id     text NOT NULL,   -- the provider's repository id; survives renames
    full_name       text NOT NULL,   -- owner/name, for display and the clone URL
    installation_id bigint,          -- GitHub App installation (ADR-0011)
    -- Gitea only: this repository's webhook secret, envelope-encrypted (ADR-0007, ADR-0011).
    -- Associated data: repo:<id>:webhook_secret. Empty until the webhook exists (M3); the intake rejects
    -- every delivery for a repository without a secret (ADR-0004).
    webhook_secret_ciphertext  bytea CHECK (octet_length(webhook_secret_ciphertext) >= 16),
    webhook_secret_nonce       bytea CHECK (octet_length(webhook_secret_nonce) = 12),
    webhook_secret_wrapped_dek bytea CHECK (octet_length(webhook_secret_wrapped_dek) = 48),
    webhook_secret_dek_nonce   bytea CHECK (octet_length(webhook_secret_dek_nonce) = 12),
    webhook_secret_kek_id      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- One repository belongs to one organization, so a webhook resolves to exactly one owner.
    UNIQUE (provider, external_id),
    UNIQUE (id, organization_id),  -- target of project's composite key
    CHECK ((provider = 'github') = (installation_id IS NOT NULL)),
    CHECK (num_nonnulls(webhook_secret_ciphertext, webhook_secret_nonce, webhook_secret_wrapped_dek,
                        webhook_secret_dek_nonce, webhook_secret_kek_id)
           IN (0, CASE provider WHEN 'gitea' THEN 5 ELSE 0 END))
);

CREATE TABLE project (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id   bigint NOT NULL REFERENCES organization,
    repository_id     bigint NOT NULL,
    -- The slug is also the hostname label (ADR-0010): unique across the installation, at most 50
    -- characters, and never shaped like a preview name (pr-<n>-...).
    slug              text NOT NULL UNIQUE
                      CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,48}[a-z0-9])?$' AND slug !~ '^pr-[0-9]+-'),
    name              text NOT NULL,
    root_dir          text NOT NULL DEFAULT '' CHECK (root_dir !~ '^/' AND root_dir !~ '(^|/)\.\.(/|$)'),
    production_branch text NOT NULL DEFAULT 'main',
    staging_branches  text[] NOT NULL DEFAULT '{}',                      -- ADR-0013
    build_timeout_s   integer NOT NULL DEFAULT 1800 CHECK (build_timeout_s BETWEEN 60 AND 7200),
    max_image_bytes   bigint NOT NULL DEFAULT 2147483648 CHECK (max_image_bytes > 0),  -- ADR-0012
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- A project may only use a repository of its own organization.
    FOREIGN KEY (repository_id, organization_id) REFERENCES git_repository (id, organization_id),
    UNIQUE (id, repository_id),  -- target of build's composite key
    CHECK (NOT production_branch = ANY (staging_branches))
);
CREATE INDEX project_organization ON project (organization_id);
CREATE INDEX project_repository ON project (repository_id, organization_id);

CREATE TABLE branch (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id bigint NOT NULL REFERENCES git_repository,
    name          text NOT NULL,
    head_sha      git_sha,  -- last head seen; the Worker asks the provider anyway (ADR-0004)
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    UNIQUE (repository_id, name)
);

CREATE TABLE pull_request (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id bigint NOT NULL REFERENCES git_repository,
    number        integer NOT NULL CHECK (number > 0),
    head_sha      git_sha NOT NULL,
    head_ref      text NOT NULL,  -- source branch, for display
    base_ref      text NOT NULL,  -- target branch
    is_fork       boolean NOT NULL,
    state         text NOT NULL CHECK (state IN ('open', 'closed')),
    -- Fork PRs only (ADR-0013). An approval is valid while approved_sha = head_sha.
    approved_sha  git_sha,
    approved_by   bigint REFERENCES app_user,
    approved_at   timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repository_id, number),
    CHECK (num_nonnulls(approved_sha, approved_by, approved_at) IN (0, 3)),
    CHECK (approved_sha IS NULL OR is_fork)
);

CREATE TABLE webhook_delivery (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider        text NOT NULL CHECK (provider IN ('gitea', 'github')),
    delivery_id     text NOT NULL,
    event           text NOT NULL,
    payload         jsonb NOT NULL,
    repository_id   bigint REFERENCES git_repository,  -- set by the Worker (data-model.md)
    received_at     timestamptz NOT NULL DEFAULT now(),
    -- Worker processing. A failure (provider API down) waits with backoff instead of spinning.
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    processed_at    timestamptz,
    UNIQUE (provider, delivery_id)  -- spec 6, ADR-0004
);
CREATE INDEX webhook_delivery_pending ON webhook_delivery (next_attempt_at) WHERE processed_at IS NULL;

CREATE TABLE audit_event (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id bigint REFERENCES organization,  -- null: platform-level action
    actor_user_id   bigint REFERENCES app_user,      -- null: system
    action          text NOT NULL,                   -- for example variable.update, fork.approve
    target          text NOT NULL,                   -- for example project:42
    detail          jsonb NOT NULL DEFAULT '{}',     -- never secret values
    at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_event_organization ON audit_event (organization_id, at);

-- 3. Nodes and runners ---------------------------------------------------------------------------

CREATE TABLE node (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                text NOT NULL UNIQUE,
    credential_hash     sha256_hash NOT NULL UNIQUE,
    -- Desired-state version (ADR-0005). D1, D2, D4, D5 and D6 add 1 in their transaction.
    desired_generation  bigint NOT NULL DEFAULT 0,
    observed_generation bigint NOT NULL DEFAULT 0,
    registered_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz,
    revoked_at          timestamptz,  -- R4 for nodes; nodes have no pause (state-machines.md)
    CHECK (observed_generation <= desired_generation)
);

CREATE TABLE registration_token (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind            text NOT NULL CHECK (kind IN ('runner', 'node')),
    -- A runner's organization and pool come from its token, never from the runner itself (ADR-0009).
    organization_id bigint REFERENCES organization,
    pool            text CHECK (pool IN ('trusted', 'untrusted')),
    token_hash      sha256_hash NOT NULL UNIQUE,
    created_by      bigint REFERENCES app_user,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz,
    CHECK (num_nonnulls(organization_id, pool) = CASE kind WHEN 'runner' THEN 2 ELSE 0 END)
);

CREATE TABLE runner (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id bigint NOT NULL REFERENCES organization,                    -- ADR-0009
    pool            text NOT NULL CHECK (pool IN ('trusted', 'untrusted')),     -- ADR-0009
    node_id         bigint REFERENCES node,
    name            text NOT NULL,
    state           text NOT NULL CHECK (state IN ('active', 'paused', 'revoked')),
    labels          text[] NOT NULL DEFAULT '{}',
    capacity        integer NOT NULL DEFAULT 1 CHECK (capacity BETWEEN 1 AND 32),
    credential_hash sha256_hash NOT NULL UNIQUE,
    registered_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz  -- online = seen in the last 30 s (state-machines.md)
);

-- 4. Environments and builds --------------------------------------------------------------------

-- Every build belongs to an environment: the "target" of ADR-0004 (project + branch or PR).
-- Development branches get one too; it never deploys (ADR-0013).
CREATE TABLE environment (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id      bigint NOT NULL REFERENCES project,
    kind            text NOT NULL CHECK (kind IN ('production', 'staging', 'development', 'preview')),
    branch_id       bigint REFERENCES branch,
    pull_request_id bigint REFERENCES pull_request,
    node_id         bigint REFERENCES node,  -- where its apps run; null until the first deployment
    -- Network cf-env-<id>. The control plane allocates the /27 and sends it in the desired state (ADR-0010).
    network_subnet  cidr UNIQUE CHECK (masklen(network_subnet) = 27),
    created_at      timestamptz NOT NULL DEFAULT now(),
    archived_at     timestamptz,
    CHECK (num_nonnulls(branch_id, pull_request_id) = 1),
    CHECK ((kind = 'preview') = (pull_request_id IS NOT NULL)),
    CHECK (kind <> 'development' OR network_subnet IS NULL),
    UNIQUE (project_id, branch_id),
    UNIQUE (project_id, pull_request_id),
    UNIQUE (id, project_id)  -- target of build's composite key
);

CREATE TABLE build (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id     bigint NOT NULL,
    repository_id  bigint NOT NULL,
    environment_id bigint NOT NULL,
    commit_sha     git_sha NOT NULL,                  -- spec 6: always an exact SHA
    trigger        text NOT NULL CHECK (trigger IN ('webhook', 'manual', 'rebuild')),
    triggered_by   bigint REFERENCES app_user,        -- manual, rebuild, fork approval
    trusted        boolean NOT NULL,                  -- false: fork PR (ADR-0013)
    state          text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    reason         text,                              -- failed or cancelled: copied from the job
    image_digest   image_digest,
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz,
    -- The repository is the project's, and the environment is the project's.
    FOREIGN KEY (project_id, repository_id) REFERENCES project (id, repository_id),
    FOREIGN KEY (environment_id, project_id) REFERENCES environment (id, project_id),
    CHECK ((state = 'succeeded') = (image_digest IS NOT NULL)),
    -- A webhook never creates an untrusted build: fork PRs build only after approval (ADR-0013).
    CHECK (trusted OR trigger <> 'webhook'),
    UNIQUE (id, state, image_digest, environment_id)  -- target of deployment's composite key
);
-- One webhook build per (project, target, SHA) = per (environment, SHA) (ADR-0004).
CREATE UNIQUE INDEX build_webhook_once ON build (environment_id, commit_sha) WHERE trigger = 'webhook';
CREATE INDEX build_open ON build (environment_id) WHERE state IN ('queued', 'running');  -- supersede
CREATE INDEX build_project ON build (project_id, id);

CREATE TABLE job (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id            bigint NOT NULL UNIQUE REFERENCES build,  -- one job per build (ADR-0003)
    state               text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt             integer NOT NULL DEFAULT 0,
    max_attempts        integer NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 10),
    priority            integer NOT NULL DEFAULT 0,
    labels              text[] NOT NULL DEFAULT '{}',  -- the runner's labels must contain these
    runner_id           bigint REFERENCES runner,
    token_hash          sha256_hash,
    lease_expires_at    timestamptz,
    deadline_at         timestamptz,
    cancel_requested_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    claimed_at          timestamptz,
    finished_at         timestamptz,
    CHECK (attempt BETWEEN 0 AND max_attempts),
    -- Queued: nobody holds it. Running: one runner holds a lease. Terminal rows keep runner and
    -- token, so a repeated complete can still be authenticated (ADR-0003 rule 6).
    CHECK (state <> 'queued' OR num_nonnulls(runner_id, token_hash, lease_expires_at, deadline_at) = 0),
    CHECK (state <> 'running' OR num_nonnulls(runner_id, token_hash, lease_expires_at, deadline_at) = 4)
);
CREATE INDEX job_claimable ON job (priority DESC, id) WHERE state = 'queued';
CREATE INDEX job_lease ON job (lease_expires_at) WHERE state = 'running';  -- reaper
CREATE INDEX job_on_runner ON job (runner_id) WHERE state = 'running';     -- capacity check

CREATE TABLE build_step (
    build_id    bigint NOT NULL REFERENCES build,
    attempt     integer NOT NULL CHECK (attempt >= 1),
    name        text NOT NULL CHECK (name IN ('clone', 'detect', 'install', 'build', 'test', 'image', 'push')),
    status      text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    cached      boolean NOT NULL DEFAULT false,  -- every vertex came from the cache (ADR-0008)
    started_at  timestamptz,
    finished_at timestamptz,
    error       text,
    PRIMARY KEY (build_id, attempt, name)
);

-- Hot log (ADR-0006). Archived to object storage when the attempt ends, then deleted.
CREATE TABLE log_chunk (
    job_id     bigint NOT NULL REFERENCES job,
    attempt    integer NOT NULL CHECK (attempt >= 1),
    seq        integer NOT NULL CHECK (seq >= 0),
    content    text NOT NULL CHECK (octet_length(content) <= 65536),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, attempt, seq)
);

CREATE TABLE artifact (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id   bigint NOT NULL REFERENCES build,
    attempt    integer NOT NULL CHECK (attempt >= 1),
    kind       text NOT NULL CHECK (kind IN ('log', 'static', 'report')),
    object_key text NOT NULL UNIQUE,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256     sha256_hash NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX artifact_one_log ON artifact (build_id, attempt) WHERE kind = 'log';

-- 5. Deployments and configuration --------------------------------------------------------------

CREATE TABLE deployment (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    environment_id   bigint NOT NULL REFERENCES environment,
    build_id         bigint NOT NULL,
    build_state      text NOT NULL DEFAULT 'succeeded' CHECK (build_state = 'succeeded'),
    image_digest     image_digest NOT NULL,
    node_id          bigint NOT NULL REFERENCES node,
    state            text NOT NULL CHECK (state IN ('pending', 'live', 'draining', 'stopped', 'failed', 'cancelled')),
    trigger          text NOT NULL CHECK (trigger IN ('auto', 'redeploy', 'rollback')),
    triggered_by     bigint REFERENCES app_user,
    reason           text,
    -- Facts reported by the agent, not states (state-machines.md rule 1).
    observed_health  text CHECK (observed_health IN ('starting', 'healthy', 'unhealthy', 'exited')),
    observed_at      timestamptz,
    route_applied_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    -- Spec 6 and the D1 guard: a succeeded build, with exactly this digest, built for this environment.
    -- A succeeded build can't change afterwards: the transition trigger and this key both block it.
    FOREIGN KEY (build_id, build_state, image_digest, environment_id)
        REFERENCES build (id, state, image_digest, environment_id)
);
CREATE UNIQUE INDEX deployment_one_live ON deployment (environment_id) WHERE state = 'live';        -- spec 11
CREATE UNIQUE INDEX deployment_one_pending ON deployment (environment_id) WHERE state = 'pending';  -- D6 first
CREATE INDEX deployment_on_node ON deployment (node_id) WHERE state IN ('pending', 'live', 'draining');
CREATE INDEX deployment_environment ON deployment (environment_id, id);
CREATE INDEX deployment_build ON deployment (build_id);

CREATE TABLE domain (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    environment_id     bigint NOT NULL REFERENCES environment,
    hostname           text NOT NULL UNIQUE  -- one environment per hostname (ADR-0010)
                       CHECK (length(hostname) <= 253
                              AND hostname ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    kind               text NOT NULL CHECK (kind IN ('generated', 'custom')),
    verification_token text,          -- custom domains: TXT _cloudforge-verify.<hostname>
    verified_at        timestamptz,   -- an unverified custom domain is never routed
    created_at         timestamptz NOT NULL DEFAULT now(),
    CHECK (kind = 'generated' OR verification_token IS NOT NULL)
);
CREATE INDEX domain_environment ON domain (environment_id);

CREATE TABLE environment_variable (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  bigint NOT NULL REFERENCES project,
    name        text NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    scope       text NOT NULL CHECK (scope IN ('production', 'staging', 'development', 'preview', 'all')),
    phase       text NOT NULL CHECK (phase IN ('build', 'runtime', 'both')),
    -- Envelope encryption (ADR-0007). Associated data: env:<project_id>:<scope>:<name>.
    ciphertext  bytea NOT NULL CHECK (octet_length(ciphertext) >= 16),  -- value + 16-byte GCM tag
    nonce       bytea NOT NULL CHECK (octet_length(nonce) = 12),
    wrapped_dek bytea NOT NULL CHECK (octet_length(wrapped_dek) = 48),  -- 32-byte DEK + tag
    dek_nonce   bytea NOT NULL CHECK (octet_length(dek_nonce) = 12),
    kek_id      text NOT NULL,
    updated_by  bigint REFERENCES app_user,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, scope, name)  -- a scoped value wins over the 'all' value of the same name
);

-- 6. State machines (state-machines.md) ---------------------------------------------------------

-- Allowed (from, to) pairs. 'new' means the row is being inserted.
-- M2 adds a unit test that compares this table with the transition map in the Python service.
CREATE TABLE state_transition (
    machine    text NOT NULL,
    from_state text NOT NULL,
    to_state   text NOT NULL,
    PRIMARY KEY (machine, from_state, to_state)
);

INSERT INTO state_transition (machine, from_state, to_state) VALUES
    ('job', 'new', 'queued'),            -- J1
    ('job', 'queued', 'running'),        -- J2
    ('job', 'running', 'succeeded'),     -- J3
    ('job', 'running', 'failed'),        -- J4, J7, J9, J10 at max
    ('job', 'running', 'cancelled'),     -- J5, J8
    ('job', 'running', 'queued'),        -- J6, J10
    ('job', 'queued', 'cancelled'),      -- J11
    ('build', 'new', 'queued'),          -- B1
    ('build', 'queued', 'running'),      -- B2
    ('build', 'running', 'succeeded'),   -- B3
    ('build', 'running', 'failed'),      -- B4
    ('build', 'queued', 'cancelled'),    -- B5
    ('build', 'running', 'cancelled'),   -- B5
    ('deployment', 'new', 'pending'),    -- D1
    ('deployment', 'pending', 'live'),   -- D2
    ('deployment', 'pending', 'failed'), -- D3
    ('deployment', 'live', 'draining'),  -- D4
    ('deployment', 'draining', 'stopped'), -- D5
    ('deployment', 'pending', 'cancelled'), -- D6
    ('runner', 'new', 'active'),         -- R1
    ('runner', 'active', 'paused'),      -- R2
    ('runner', 'paused', 'active'),      -- R3
    ('runner', 'active', 'revoked'),     -- R4
    ('runner', 'paused', 'revoked');     -- R4

CREATE FUNCTION check_state_transition() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_state text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        old_state := 'new';
    ELSE
        old_state := OLD.state;
    END IF;
    IF old_state IS DISTINCT FROM NEW.state AND NOT EXISTS (
        SELECT 1 FROM state_transition
        WHERE machine = TG_TABLE_NAME AND from_state = old_state AND to_state = NEW.state
    ) THEN
        RAISE EXCEPTION 'illegal % transition: % -> %', TG_TABLE_NAME, old_state, NEW.state
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER job_state BEFORE INSERT OR UPDATE OF state ON job
    FOR EACH ROW EXECUTE FUNCTION check_state_transition();
CREATE TRIGGER build_state BEFORE INSERT OR UPDATE OF state ON build
    FOR EACH ROW EXECUTE FUNCTION check_state_transition();
CREATE TRIGGER deployment_state BEFORE INSERT OR UPDATE OF state ON deployment
    FOR EACH ROW EXECUTE FUNCTION check_state_transition();
CREATE TRIGGER runner_state BEFORE INSERT OR UPDATE OF state ON runner
    FOR EACH ROW EXECUTE FUNCTION check_state_transition();

-- One event row per transition, written by the same transaction (state-machines.md rule 3).
-- from_state = to_state for events that change no state, such as cancel_requested.
CREATE TABLE job_event (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     bigint NOT NULL REFERENCES job,
    attempt    integer NOT NULL CHECK (attempt >= 0),
    from_state text NOT NULL,
    to_state   text NOT NULL,
    reason     text NOT NULL,
    actor      actor NOT NULL,
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX job_event_job ON job_event (job_id, id);

CREATE TABLE build_event (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id   bigint NOT NULL REFERENCES build,
    from_state text NOT NULL,
    to_state   text NOT NULL,
    reason     text NOT NULL,
    actor      actor NOT NULL,
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX build_event_build ON build_event (build_id, id);

CREATE TABLE deployment_event (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_id bigint NOT NULL REFERENCES deployment,
    from_state    text NOT NULL,
    to_state      text NOT NULL,
    reason        text NOT NULL,
    actor         actor NOT NULL,
    at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployment_event_deployment ON deployment_event (deployment_id, id);

CREATE TABLE runner_event (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    runner_id  bigint NOT NULL REFERENCES runner,
    from_state text NOT NULL,
    to_state   text NOT NULL,
    reason     text NOT NULL,
    actor      actor NOT NULL,
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runner_event_runner ON runner_event (runner_id, id);

-- 7. Grants -------------------------------------------------------------------------------------

-- The app can read and write rows, but can't change the schema, disable triggers, rewrite history
-- or edit the allowed transitions.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO cloudforge_app;
REVOKE UPDATE, DELETE ON job_event, build_event, deployment_event, runner_event, audit_event FROM cloudforge_app;
REVOKE INSERT, UPDATE, DELETE ON state_transition FROM cloudforge_app;
