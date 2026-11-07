package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/moby/buildkit/client"

	"cloudforge/cfnode/internal/api"
)

func TestCheckoutFetchesExactlyTheCommit(t *testing.T) {
	origin := newRepo(t)
	write(t, origin, "app.txt", "v1")
	first := commit(t, origin) // not the branch tip: Checkout must fetch it by its SHA
	write(t, origin, "app.txt", "v2")
	commit(t, origin)

	dir := filepath.Join(t.TempDir(), "checkout")
	got, err := Checkout(t.Context(), api.Source{CloneURL: "file://" + origin, CommitSHA: first}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if head := gitOut(t, dir, "rev-parse", "HEAD"); head != first {
		t.Errorf("HEAD = %s, want the requested commit %s", head, first)
	}
	if content, _ := os.ReadFile(filepath.Join(got, "app.txt")); string(content) != "v1" {
		t.Errorf("app.txt = %q, want v1", content)
	}
	if n := gitOut(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("the checkout has %s commits, want 1 (depth 1)", n)
	}
}

func TestCheckoutRefusesARootDirThatLeavesTheCheckout(t *testing.T) {
	origin := newRepo(t)
	if err := os.Symlink("/", filepath.Join(origin, "app")); err != nil { // a hostile repository's "app" folder
		t.Fatal(err)
	}
	sha := commit(t, origin)

	src := api.Source{CloneURL: "file://" + origin, CommitSHA: sha, RootDir: "app"}
	if _, err := Checkout(t.Context(), src, filepath.Join(t.TempDir(), "checkout")); err == nil ||
		!strings.Contains(err.Error(), "leads out of the checkout") {
		t.Fatalf("err = %v, want the root_dir refused", err)
	}
}

// TestImageBuildsAndPushes needs the stack's buildkitd and registry: `make test` runs it in the test-build container.
func TestImageBuildsAndPushes(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("needs buildkitd: `make test` runs it in the test-build container")
	}
	password, err := os.ReadFile("/run/secrets/registry_runner")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, "Dockerfile", "FROM busybox:1.37\nRUN echo built > /built\n")
	buildkitd, err := client.New(t.Context(), addr)
	if err != nil {
		t.Fatal(err)
	}
	defer buildkitd.Close()

	registry := os.Getenv("CF_REGISTRY")
	login := &Login{Host: registry, Username: "runner", Password: string(password)}
	var log strings.Builder
	digest, err := Image(t.Context(), buildkitd, dir, registry+"/cf-test/image:1", login, &log)
	if err != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
		t.Fatalf("digest %q, err %v, log:\n%s", digest, err, log.String())
	}
	t.Logf("pushed %s, log:\n%s", digest, log.String())
}

// newRepo makes an empty Git repository in a temporary folder, away from this machine's Git settings.
func newRepo(t *testing.T) string {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull) // this machine's Git settings stay out of the tests
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitOut(t, dir, "init", "--quiet")
	return dir
}

// commit commits everything in dir and returns the new commit's SHA.
func commit(t *testing.T, dir string) string {
	gitOut(t, dir, "add", "--all")
	gitOut(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "next")
	return gitOut(t, dir, "rev-parse", "HEAD")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
