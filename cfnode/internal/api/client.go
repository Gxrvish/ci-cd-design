package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrFenced means this attempt no longer owns its job (409 on a job call). Stop the attempt and drop its unsent
// data (ADR-0003 rule 3).
var ErrFenced = errors.New("fenced")

// ErrRejected means the API refused the request itself (a 4xx other than 401 and 409, such as 422). That is a
// bug on the runner's side, so retrying won't help.
var ErrRejected = errors.New("rejected")

// maxAnswer caps how much of an answer is read. A claim, the biggest answer, is a few KiB.
const maxAnswer = 1 << 20

// Client calls the node API for one runner: each method is one POST of contracts/v1.yaml.
//
// A refusal comes back as an error that wraps ErrUnauthorized, ErrFenced or ErrRejected; check it with errors.Is.
// Any other error, such as a network failure or a 5xx, is temporary: the caller may try again.
type Client struct {
	URL        string       // the API's base URL, such as http://api:8000
	Credential string       // the runner's credential, for claims
	HTTP       *http.Client // sends the requests; its Timeout bounds each call
}

// Registration is the body of POST /v1/runners/register.
type Registration struct {
	Name     string   `json:"name"`
	Labels   []string `json:"labels,omitempty"` // left out when empty: Go sends a nil slice as null, which the API refuses
	Capacity int      `json:"capacity"`
}

// Credential is what register returns once: the new runner's id and its credential.
type Credential struct {
	ID         int64  `json:"id"`
	Credential string `json:"credential"`
}

// Completion is the body of POST .../complete: how the attempt ended. Empty fields are left out of the JSON,
// because the API refuses an empty digest or reason.
type Completion struct {
	Result      string `json:"result"`                 // succeeded, failed or cancelled
	ImageDigest string `json:"image_digest,omitempty"` // with succeeded
	Reason      string `json:"reason,omitempty"`       // with failed: step_failed:<step>, timeout or image_too_large
}

// Register trades a one-time registration token for this runner's credential (R1).
func (c *Client) Register(ctx context.Context, token string, r Registration) (Credential, error) {
	var credential Credential
	_, err := c.post(ctx, "/v1/runners/register", token, r, &credential)
	return credential, err
}

// Claim asks for one job (J2) and returns nil when there is nothing to run (204).
// With this method, *Client is the Claimer that runner.Loop needs.
func (c *Client) Claim(ctx context.Context) (*Claim, error) {
	var job Claim
	status, err := c.post(ctx, "/v1/jobs/claim", c.Credential, nil, &job)
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	return &job, nil
}

// AppendLog sends chunk seq of the attempt's log (ADR-0006). It returns whether someone asked to cancel the job.
func (c *Client) AppendLog(ctx context.Context, job Claim, seq int, text string) (bool, error) {
	chunk := struct { // a type without a name, for this one body
		Seq  int    `json:"seq"`
		Text string `json:"text"`
	}{seq, text}
	var ack struct {
		CancelRequested bool `json:"cancel_requested"`
	}
	_, err := c.post(ctx, jobPath(job, "logs"), job.JobToken, chunk, &ack)
	return ack.CancelRequested, err
}

// Complete reports how the attempt ended (J3, J4 or J5). The API accepts the same completion again, so a retry
// after a lost answer is safe (ADR-0003 rule 6).
func (c *Client) Complete(ctx context.Context, job Claim, done Completion) error {
	_, err := c.post(ctx, jobPath(job, "complete"), job.JobToken, done, nil)
	return err
}

// jobPath is the URL path of one call of the job's current attempt, such as /v1/jobs/7/attempts/1/logs.
func jobPath(job Claim, call string) string {
	return fmt.Sprintf("/v1/jobs/%d/attempts/%d/%s", job.JobID, job.Attempt, call)
}

// post sends body as JSON (no body when it is nil), with secret as the bearer token. It decodes a 2xx answer's
// JSON into out, unless out is nil, and returns the status code. Any other answer becomes an error.
func (c *Client) post(ctx context.Context, path, secret string, body, out any) (int, error) {
	payload := io.Reader(http.NoBody)
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = bytes.NewReader(data)
	}
	// The request carries ctx: cancelling ctx aborts the call, like a timeout.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err // no answer at all: temporary
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return 0, err
	}

	var refusal error
	switch code := resp.StatusCode; {
	case code == http.StatusNoContent:
		return code, nil
	case code >= 200 && code < 300:
		if out != nil {
			if err := json.Unmarshal(answer, out); err != nil {
				return 0, fmt.Errorf("POST %s: %w", path, err)
			}
		}
		return code, nil
	case code == http.StatusUnauthorized:
		refusal = ErrUnauthorized
	case code == http.StatusConflict:
		refusal = ErrFenced
	case code >= 400 && code < 500:
		refusal = ErrRejected
	default: // 5xx, or a status the contract doesn't use: temporary
		return 0, fmt.Errorf("POST %s: %s: %s", path, resp.Status, answer)
	}
	// %w wraps the refusal: errors.Is(err, ErrFenced) finds it inside this longer message.
	return 0, fmt.Errorf("POST %s: %w (%s): %s", path, refusal, resp.Status, answer)
}
