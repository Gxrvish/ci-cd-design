// Package api holds the node API's types and its client (contracts/v1.yaml).
package api

import "errors"

// ErrUnauthorized means the control plane refused the credential or token (401). Retrying won't help.
var ErrUnauthorized = errors.New("unauthorized")

// Claim is one job, as POST /v1/jobs/claim returns it.
type Claim struct {
	JobID                    int64       `json:"job_id"`
	Attempt                  int         `json:"attempt"`
	JobToken                 string      `json:"job_token"`
	LeaseTTLSeconds          int         `json:"lease_ttl_s"`
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
