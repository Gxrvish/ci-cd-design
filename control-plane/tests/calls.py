"""API calls the tests share."""

SHA = "3f786850e387550fdab836ed7e6dc881de23001b"


def bearer(secret):
    return {"Authorization": f"Bearer {secret}"}


def connect_repository(client, org="default"):
    body = {"provider": "gitea", "external_id": "1", "full_name": "cloudforge/hello"}
    return client.post(f"/api/orgs/{org}/repositories", json=body)


def create_project(client, slug="hello", repository_id=None):
    body = {"slug": slug, "name": "Hello", "repository_id": repository_id or connect_repository(client).json()["id"]}
    return client.post("/api/orgs/default/projects", json=body)


def queue_build(client):
    """A queued manual build of a new project `hello`."""
    create_project(client)
    return client.post("/api/projects/hello/builds", json={"commit_sha": SHA}).json()


def runner_token(client, pool="trusted"):
    return client.post("/api/orgs/default/runner-tokens", json={"pool": pool}).json()["token"]


def register_runner(client, pool="trusted"):
    """A new runner's auth header."""
    response = client.post(
        "/v1/runners/register", json={"name": "r1", "capacity": 1}, headers=bearer(runner_token(client, pool))
    )
    return bearer(response.json()["credential"])


def claimed_job(client):
    """Queues a build and claims it: the claim, and the URL prefix and auth header of its job calls."""
    build = queue_build(client)
    job = client.post("/v1/jobs/claim", headers=register_runner(client)).json()
    return build, f"/v1/jobs/{job['job_id']}/attempts/{job['attempt']}", bearer(job["job_token"])
