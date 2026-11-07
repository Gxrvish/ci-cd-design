package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// request is what the fake API received.
type request struct {
	path, auth string
	body       map[string]any // the JSON body, or nil when there was none
}

// fakeAPI starts a real HTTP server on a free local port. It answers every call with status and answer, and
// records the request. The server stops when the test ends.
func fakeAPI(t *testing.T, status int, answer string) (*Client, *request) {
	t.Helper()
	seen := &request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.auth = r.URL.Path, r.Header.Get("Authorization")
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			if err := json.Unmarshal(data, &seen.body); err != nil {
				t.Errorf("body %q is not JSON: %v", data, err)
			}
		}
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return &Client{URL: server.URL, Credential: "runner-credential", HTTP: server.Client()}, seen
}

func TestClaimReturnsTheJobOrNothing(t *testing.T) {
	c, seen := fakeAPI(t, 200, `{"job_id": 7, "attempt": 1, "job_token": "job-token", "source": {"commit_sha": "3f78"}}`)

	job, err := c.Claim(t.Context())
	if err != nil || job == nil || job.JobID != 7 || job.JobToken != "job-token" || job.Source.CommitSHA != "3f78" {
		t.Fatalf("Claim() = %+v, %v", job, err)
	}
	if seen.path != "/v1/jobs/claim" || seen.auth != "Bearer runner-credential" || seen.body != nil {
		t.Fatalf("sent %+v: want POST /v1/jobs/claim with the credential and no body", seen)
	}

	c, _ = fakeAPI(t, 204, "")
	if job, err := c.Claim(t.Context()); job != nil || err != nil {
		t.Fatalf("after 204, Claim() = %v, %v; want nil, nil: nothing to run", job, err)
	}
}

func TestRefusalsBecomeErrorsOfTheirKind(t *testing.T) {
	for status, want := range map[int]error{401: ErrUnauthorized, 409: ErrFenced, 413: ErrRejected, 422: ErrRejected} {
		c, _ := fakeAPI(t, status, `{"detail": "no"}`)
		if _, err := c.Claim(t.Context()); !errors.Is(err, want) {
			t.Errorf("after %d, err = %v; want one that wraps %v", status, err, want)
		}
	}

	c, _ := fakeAPI(t, 503, "down")
	_, err := c.Claim(t.Context())
	if err == nil || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrFenced) || errors.Is(err, ErrRejected) {
		t.Fatalf("after 503, err = %v; want a plain error, which callers retry", err)
	}
}

func TestJobCallsUseTheAttemptsTokenAndPath(t *testing.T) {
	job := Claim{JobID: 7, Attempt: 2, JobToken: "job-token"}

	c, seen := fakeAPI(t, 202, `{"cancel_requested": true}`)
	cancel, err := c.AppendLog(t.Context(), job, 3, "hello\n")
	if err != nil || !cancel {
		t.Fatalf("AppendLog() = %v, %v; want the cancel flag the API sent", cancel, err)
	}
	if seen.path != "/v1/jobs/7/attempts/2/logs" || seen.auth != "Bearer job-token" ||
		seen.body["seq"] != 3.0 || seen.body["text"] != "hello\n" { // JSON numbers decode as float64
		t.Fatalf("sent %+v", seen)
	}

	c, seen = fakeAPI(t, 200, "")
	if err := c.Complete(t.Context(), job, Completion{Result: "succeeded", ImageDigest: "sha256:ab"}); err != nil {
		t.Fatal(err)
	}
	if seen.path != "/v1/jobs/7/attempts/2/complete" || len(seen.body) != 2 || seen.body["image_digest"] != "sha256:ab" {
		t.Fatalf("sent %+v: want result and image_digest only, because the API refuses an empty reason", seen)
	}
}

func TestRegisterSendsTheTokenAndNoNullLabels(t *testing.T) {
	c, seen := fakeAPI(t, 201, `{"id": 4, "credential": "new-credential"}`)

	credential, err := c.Register(t.Context(), "registration-token", Registration{Name: "r1", Capacity: 2})
	if err != nil || credential != (Credential{ID: 4, Credential: "new-credential"}) {
		t.Fatalf("Register() = %+v, %v", credential, err)
	}
	if seen.auth != "Bearer registration-token" || seen.body["name"] != "r1" || seen.body["capacity"] != 2.0 {
		t.Fatalf("sent %+v", seen)
	}
	if labels, sent := seen.body["labels"]; sent {
		t.Fatalf("sent labels %v: leave them out when empty, because the API refuses null", labels)
	}
}
