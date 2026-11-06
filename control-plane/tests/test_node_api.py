from cloudforge.credentials import digest
from tests.calls import SHA, bearer, queue_build, register_runner, runner_token


def claim(client, headers):
    return client.post("/v1/jobs/claim", headers=headers)


def test_a_token_registers_one_runner(client):
    token = runner_token(client)
    first = client.post("/v1/runners/register", json={"name": "r1", "capacity": 2}, headers=bearer(token))

    assert first.status_code == 201 and first.json()["credential"]
    assert (
        client.post("/v1/runners/register", json={"name": "r2", "capacity": 2}, headers=bearer(token)).status_code
        == 401
    )


def test_expired_and_node_tokens_register_nothing(client, owner):
    owner.execute(
        "INSERT INTO registration_token (kind, organization_id, pool, token_hash, expires_at)"
        " SELECT 'runner', id, 'trusted', %s, now() - interval '1 second' FROM organization",
        (digest("expired"),),
    )
    owner.execute(
        "INSERT INTO registration_token (kind, token_hash, expires_at) VALUES ('node', %s, now() + interval '1 hour')",
        (digest("node"),),
    )

    for token in ("expired", "node", "unknown"):
        assert (
            client.post("/v1/runners/register", json={"name": "r", "capacity": 1}, headers=bearer(token)).status_code
            == 401
        )


def test_a_queued_build_is_claimed_once(client):
    build = queue_build(client)
    runner = register_runner(client)
    response = claim(client, runner)

    assert response.status_code == 200
    job = response.json()
    assert (job["attempt"], job["lease_ttl_s"], job["heartbeat_interval_s"], job["secrets"]) == (1, 30, 10, [])
    assert job["source"] == {
        "clone_url": "http://gitea:3000/cloudforge/hello.git",
        "commit_sha": SHA,
        "clone_token": None,
        "root_dir": "",
    }
    assert job["image"]["ref"] == f"registry:5000/default/hello:{SHA[:12]}-b{build['id']}-a1"
    assert client.get(f"/api/builds/{build['id']}").json()["state"] == "running"
    assert claim(client, runner).status_code == 204


def test_trusted_builds_never_go_to_the_untrusted_pool(client):
    queue_build(client)

    assert claim(client, register_runner(client, pool="untrusted")).status_code == 204


def test_paused_unknown_and_missing_runners(client, owner):
    queue_build(client)
    runner = register_runner(client)
    owner.execute("UPDATE runner SET state = 'paused'")

    assert claim(client, runner).status_code == 204
    assert claim(client, bearer("unknown")).status_code == 401
    assert claim(client, {}).status_code == 401
