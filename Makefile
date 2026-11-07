# Local development (docs/operations/local-setup.md).
#
# Make basics: `target: prerequisites`, then the recipe: shell lines, each indented by a tab. `$@` is the
# target, `$<` its first prerequisite, and `%` matches any name. A file target is made only when it is missing
# or older than its prerequisites, so `make init` keeps existing secrets. .PHONY targets are commands, not files.
SECRETS := secrets/pg_superuser_password secrets/pg_owner_password secrets/pg_app_password \
	secrets/registry_runner secrets/registry_htpasswd

.PHONY: init up down test test-go clean

init: $(SECRETS)

# The folder is private to you; the files are readable inside the containers they are mounted into.
secrets/%:
	@mkdir -p secrets && chmod 700 secrets
	@head -c 32 /dev/urandom | od -An -vtx1 | tr -d ' \n' > $@ && chmod 444 $@
	@echo "created $@"

# The registry accepts only bcrypt hashes. htpasswd comes with apache2-utils.
secrets/registry_htpasswd: secrets/registry_runner
	@htpasswd -iBn runner < $< > $@.new && chmod 444 $@.new && mv -f $@.new $@
	@echo "created $@"

up: init
	docker compose up --detach --build --wait

down:
	docker compose down

test: init test-go
	docker compose run --rm --build test
	docker compose run --rm --build test-build

# Go runs on the host: gofmt, vet, then the tests with the race detector.
# The BuildKit test needs buildkitd, so it is also compiled for the test-build container (make test).
test-go:
	@cd cfnode && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }
	cd cfnode && go vet ./... && go test -race ./...
	cd cfnode && CGO_ENABLED=0 go test -c -o bin/build.test ./internal/build

clean:
	@printf 'Delete the CloudForge database volume? [y/N] ' && read answer && [ "$$answer" = y ]
	docker compose down --volumes
