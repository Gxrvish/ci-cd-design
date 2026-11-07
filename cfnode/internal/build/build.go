// Package build turns one commit into a pushed image: check it out, then build and push it with buildkitd (ADR-0008).
package build

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc"

	"cloudforge/cfnode/internal/api"
)

// Checkout fetches exactly the source's commit into dir: depth 1, no submodules, no LFS (ADR-0008).
// It returns the build context, the source's root_dir inside the checkout.
func Checkout(ctx context.Context, src api.Source, dir string) (string, error) {
	if err := git(ctx, "", "init", "--quiet", dir); err != nil {
		return "", err
	}
	if err := git(ctx, dir, "fetch", "--quiet", "--depth=1", "--no-tags", "--no-recurse-submodules",
		src.CloneURL, src.CommitSHA); err != nil {
		return "", err
	}
	if err := git(ctx, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return contextDir(dir, src.RootDir)
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0") // fail instead of waiting for a password
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, out)
	}
	return nil
}

// contextDir resolves rootDir inside the checkout. The repository is hostile: a symlink there could otherwise point
// the build context at the runner's own files, and the build would copy them into the image.
func contextDir(checkout, rootDir string) (string, error) {
	base, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(base, rootDir))
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(base, dir); err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("root_dir %q leads out of the checkout", rootDir)
	}
	return dir, nil
}

// Login hands buildkitd the account for one registry, and nothing else. It offers no token authority, so buildkitd
// fetches registry tokens itself, from its own network, never through the runner (threat 32).
type Login struct {
	auth.UnimplementedAuthServer // every other call answers "unimplemented"
	Host, Username, Password     string
}

// Register adds the login to the build's session, which buildkitd calls back during the build.
func (l *Login) Register(server *grpc.Server) { auth.RegisterAuthServer(server, l) }

// Credentials answers buildkitd's question "which account for this registry?": ours for our registry, none elsewhere.
func (l *Login) Credentials(_ context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	if req.Host != l.Host {
		return &auth.CredentialsResponse{}, nil
	}
	return &auth.CredentialsResponse{Username: l.Username, Secret: l.Password}, nil
}

// Image builds the Dockerfile in dir with buildkitd, pushes the image as ref and returns its digest.
// Progress goes to log as plain text, as `buildctl --progress plain` shows it.
func Image(ctx context.Context, buildkitd *client.Client, dir, ref string, login *Login, log io.Writer) (string, error) {
	files, err := fsutil.NewFS(dir)
	if err != nil {
		return "", err
	}
	display, err := progressui.NewDisplay(log, progressui.PlainMode)
	if err != nil {
		return "", err
	}
	status := make(chan *client.SolveStatus)
	var shown sync.WaitGroup
	shown.Go(func() { _, _ = display.UpdateFrom(context.WithoutCancel(ctx), status) }) // until Solve closes status

	res, err := buildkitd.Solve(ctx, nil, client.SolveOpt{
		Frontend:    "dockerfile.v0",
		LocalMounts: map[string]fsutil.FS{"context": files, "dockerfile": files},
		Exports: []client.ExportEntry{{
			Type:  client.ExporterImage,
			Attrs: map[string]string{"name": ref, "push": "true"},
		}},
		Session: []session.Attachable{login},
	}, status)
	shown.Wait()
	if err != nil {
		return "", err
	}
	return res.ExporterResponse[exptypes.ExporterImageDigestKey], nil
}
