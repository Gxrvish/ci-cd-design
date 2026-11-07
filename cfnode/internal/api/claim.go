// Package api is the runner's side of the node API (contracts/v1.yaml): the JSON messages it exchanges with
// the control plane.
package api

import "errors"

// ErrUnauthorized means the control plane refused the credential or token (401). Retrying won't help.
var ErrUnauthorized = errors.New("unauthorized")

// Claim is one job, as POST /v1/jobs/claim returns it.
//
// Each field's tag, such as `json:"job_id"`, names its key in the JSON. encoding/json fills the struct from
// the response body by these names, the way Pydantic fills a model.
type Claim struct {
	JobID                    int64       `json:"job_id"`
	Attempt                  int         `json:"attempt"`     // 1 on the job's first claim, +1 on each retry
	JobToken                 string      `json:"job_token"`   // authenticates this attempt's calls: logs, complete
	LeaseTTLSeconds          int         `json:"lease_ttl_s"` // the claim holds the job this long without a heartbeat
	HeartbeatIntervalSeconds int         `json:"heartbeat_interval_s"`
	TimeoutSeconds           int         `json:"timeout_s"`
	LogLimitBytes            int64       `json:"log_limit_bytes"`
	Source                   Source      `json:"source"`
	Image                    ImageTarget `json:"image"`
	Secrets                  []Secret    `json:"secrets"`
}

// Source is the exact commit to build.
type Source struct {
	CloneURL   string  `json:"clone_url"`
	CommitSHA  string  `json:"commit_sha"`
	CloneToken *string `json:"clone_token"` // nil for a public repository
	RootDir    string  `json:"root_dir"`
}

// ImageTarget is where the image goes: <registry>/<org>/<project>:<sha12>-b<build>-a<attempt> (ADR-0008).
type ImageTarget struct {
	Ref      string `json:"ref"`
	MaxBytes int64  `json:"max_bytes"`
}

// Secret is a build-phase variable, for BuildKit secret mounts only (ADR-0007).
type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
