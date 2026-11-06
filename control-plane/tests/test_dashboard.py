SHA = "3f786850e387550fdab836ed7e6dc881de23001b"


def connect_repository(client, org="default"):
    body = {"provider": "gitea", "external_id": "1", "full_name": "cloudforge/hello"}
    return client.post(f"/api/orgs/{org}/repositories", json=body)


def create_project(client, slug="hello", repository_id=None):
    body = {"slug": slug, "name": "Hello", "repository_id": repository_id or connect_repository(client).json()["id"]}
    return client.post("/api/orgs/default/projects", json=body)


def test_a_repository_connects_once(client):
    first = connect_repository(client)

    assert first.status_code == 201
    assert first.json()["full_name"] == "cloudforge/hello"
    assert connect_repository(client).status_code == 409


def test_a_project_gets_its_production_environment(client, owner):
    response = create_project(client)

    assert response.status_code == 201
    assert owner.execute(
        "SELECT e.kind, b.name FROM environment e JOIN branch b ON b.id = e.branch_id WHERE e.project_id = %s",
        (response.json()["id"],),
    ).fetchall() == [("production", "main")]


def test_project_input_is_checked_by_the_schema(client):
    repository_id = connect_repository(client).json()["id"]

    assert create_project(client, "hello", repository_id).status_code == 201
    assert create_project(client, "hello", repository_id).status_code == 409  # slug taken
    assert create_project(client, "pr-1-hello", repository_id).json()["detail"][0]["msg"] == "project_slug_check"
    assert create_project(client, "other", repository_id=999).status_code == 404


def test_a_manual_build_is_queued_and_readable(client):
    create_project(client)
    created = client.post("/api/projects/hello/builds", json={"commit_sha": SHA})

    assert created.status_code == 201
    build = client.get(f"/api/builds/{created.json()['id']}").json()
    assert (build["project"], build["commit_sha"], build["state"], build["attempt"]) == ("hello", SHA, "queued", 0)


def test_unknown_or_malformed_requests(client):
    create_project(client)

    assert client.post("/api/projects/hello/builds", json={"commit_sha": "main"}).status_code == 422
    assert client.post("/api/projects/nope/builds", json={"commit_sha": SHA}).status_code == 404
    assert client.get("/api/builds/999").status_code == 404
    assert connect_repository(client, org="nope").status_code == 404
