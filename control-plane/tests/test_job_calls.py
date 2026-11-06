from tests.calls import bearer, claimed_job

DIGEST = "sha256:" + "ab" * 32


def test_chunks_are_stored_once_and_read_back_in_order(client):
    build, job, token = claimed_job(client)

    for seq, text in [(1, "world\n"), (0, "hello\n"), (0, "repeat\n")]:
        assert client.post(f"{job}/logs", json={"seq": seq, "text": text}, headers=token).json() == {
            "cancel_requested": False
        }
    log = client.get(f"/api/builds/{build['id']}/attempts/1/log")

    assert (log.headers["content-type"], log.text) == ("text/plain; charset=utf-8", "hello\nworld\n")
    assert client.get("/api/builds/999/attempts/1/log").status_code == 404


def test_job_calls_need_this_attempts_token(client):
    _, job, token = claimed_job(client)

    assert client.post(f"{job}/logs", json={"seq": 0, "text": "x"}, headers=bearer("wrong")).status_code == 401
    assert (
        client.post(
            job.replace("/attempts/1", "/attempts/2") + "/logs", json={"seq": 0, "text": "x"}, headers=token
        ).status_code
        == 409
    )
    assert client.post(f"{job}/logs", json={"seq": 0, "text": "x" * 65537}, headers=token).status_code == 413
    assert client.post(f"{job}/logs", json={"seq": 0, "text": "nul\u0000"}, headers=token).status_code == 422


def test_success_finishes_job_and_build_once(client):
    build, job, token = claimed_job(client)
    done = {"result": "succeeded", "image_digest": DIGEST}

    assert client.post(f"{job}/complete", json=done, headers=token).status_code == 200
    assert client.post(f"{job}/complete", json=done, headers=token).status_code == 200  # a retry
    assert (
        client.post(f"{job}/complete", json={"result": "failed", "reason": "timeout"}, headers=token).status_code == 409
    )
    assert client.post(f"{job}/logs", json={"seq": 0, "text": "late"}, headers=token).status_code == 409
    finished = client.get(f"/api/builds/{build['id']}").json()
    assert (finished["state"], finished["image_digest"]) == ("succeeded", DIGEST)


def test_failure_keeps_its_reason(client):
    build, job, token = claimed_job(client)

    assert (
        client.post(
            f"{job}/complete", json={"result": "failed", "reason": "step_failed:build"}, headers=token
        ).status_code
        == 200
    )
    failed = client.get(f"/api/builds/{build['id']}").json()
    assert (failed["state"], failed["reason"], failed["image_digest"]) == ("failed", "step_failed:build", None)


def test_completions_the_contract_forbids(client):
    _, job, token = claimed_job(client)

    for body in ({"result": "succeeded"}, {"result": "failed"}, {"result": "succeeded", "image_digest": "latest"}):
        assert client.post(f"{job}/complete", json=body, headers=token).status_code == 422
    assert (
        client.post(f"{job}/complete", json={"result": "cancelled"}, headers=token).status_code == 409
    )  # not asked to
