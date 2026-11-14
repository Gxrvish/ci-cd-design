// Command cfnode is CloudForge's node program. One binary plays several roles, chosen by its argument:
//
//	cfnode runner    claims jobs from the control plane and builds them, until SIGINT or SIGTERM
//	cfnode register  trades a one-time registration token for a runner's credential, and prints it
//
// `cfnode agent` arrives with slice 4. Settings come from environment variables, and secrets from files in
// /run/secrets, where Compose mounts them (docs/operations/local-setup.md):
//
//	CF_API_URL             both: the control plane's address, such as http://api:8000
//	CF_REGISTRATION_TOKEN  register: the token from POST /api/orgs/{org}/runner-tokens
//	BUILDKIT_HOST          runner: buildkitd's address, such as unix:///run/user/61000/buildkit/buildkitd.sock
//	CF_REGISTRY            runner: the registry that images go to, such as registry:5000
//	runner_credential      runner, a secret file: what `cfnode register` printed
//	registry_runner        runner, a secret file: the password of the registry's account `runner`
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/buildkit/client"

	"cloudforge/cfnode/internal/api"
	"cloudforge/cfnode/internal/build"
	"cloudforge/cfnode/internal/runner"
)

const (
	capacity    = 1                // jobs at once: what the runner registers, and what its loop allows
	callTimeout = 10 * time.Second // the longest that one API call may take
)

// main is where the program starts, like Python's `if __name__ == "__main__":`.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cfnode:", err)
		os.Exit(1)
	}
}

// run picks the command. ctx is the stop signal for everything the command starts: NotifyContext cancels it at
// Ctrl-C (SIGINT) and at `docker stop` (SIGTERM), like a signal handler that sets a threading.Event.
func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch strings.Join(args, " ") { // the command, and nothing after it
	case "runner":
		return serve(ctx)
	case "register":
		return register(ctx)
	default:
		return errors.New("usage: cfnode runner | cfnode register")
	}
}

// register is `cfnode register`: it trades a registration token for the credential of a new runner, named after
// this machine, and prints the credential (R1). Keep it: the control plane stores only its hash.
func register(ctx context.Context) error {
	env, err := settings("CF_API_URL", "CF_REGISTRATION_TOKEN")
	if err != nil {
		return err
	}
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	control := &api.Client{URL: env["CF_API_URL"], HTTP: &http.Client{Timeout: callTimeout}}
	registration := api.Registration{Name: name, Capacity: capacity}
	credential, err := control.Register(ctx, env["CF_REGISTRATION_TOKEN"], registration)
	if err != nil {
		return err
	}
	fmt.Println(credential.Credential)
	return nil
}

// serve is `cfnode runner`: it claims jobs and builds them with buildkitd, until ctx is cancelled.
// This is where the runner's parts meet: the API client, the claim loop, the executor and the build engine.
func serve(ctx context.Context) error {
	env, err := settings("CF_API_URL", "BUILDKIT_HOST", "CF_REGISTRY")
	if err != nil {
		return err
	}
	credential, err := secret("runner_credential")
	if err != nil {
		return err
	}
	password, err := secret("registry_runner")
	if err != nil {
		return err
	}
	buildkitd, err := client.New(ctx, env["BUILDKIT_HOST"]) // connects at the first build, not here
	if err != nil {
		return err
	}
	defer buildkitd.Close()

	control := &api.Client{URL: env["CF_API_URL"], Credential: credential, HTTP: &http.Client{Timeout: callTimeout}}
	// One shared registry account until M8's job-scoped tokens (ADR-0009, docs/stubs.md).
	login := &build.Login{Host: env["CF_REGISTRY"], Username: "runner", Password: password}
	builder := &runner.Builder{
		API:      control,
		Checkout: build.Checkout,
		// A closure: build.Image with this runner's buildkitd and login filled in, like functools.partial.
		Image: func(ctx context.Context, dir, ref string, log io.Writer) (string, error) {
			return build.Image(ctx, buildkitd, dir, ref, login, log)
		},
	}
	slog.Info("runner started", "api", control.URL, "capacity", capacity)
	// control is the loop's Claimer, and builder its Executor. The timings are the contract's (contracts/v1.yaml).
	config := runner.Config{Capacity: capacity, PollInterval: time.Second, MaxBackoff: 2 * time.Second}
	return runner.Loop(ctx, config, control, builder)
}

// settings returns the values of the named environment variables, or an error that lists the ones not set.
func settings(names ...string) (map[string]string, error) {
	values := map[string]string{}
	var missing []string
	for _, name := range names {
		if values[name] = os.Getenv(name); values[name] == "" {
			missing = append(missing, name)
		}
	}
	if missing != nil {
		return nil, fmt.Errorf("not set in the environment: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

// secret reads one secret file, such as /run/secrets/runner_credential, without the newline at its end.
func secret(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join("/run/secrets", name))
	return strings.TrimSpace(string(data)), err
}
