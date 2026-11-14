# Local development (docs/operations/local-setup.md).
#
# Make basics: `target: prerequisites`, then the recipe: shell lines, each indented by a tab. `$@` is the
# target, `$<` its first prerequisite, and `%` matches any name. A file target is made only when it is missing
# or older than its prerequisites, so `make init` keeps existing secrets. .PHONY targets are commands, not files.
SECRETS := secrets/pg_superuser_password secrets/pg_owner_password secrets/pg_app_password \
	secrets/registry_runner secrets/registry_htpasswd

# The dashboard's address on this machine. `-include` reads .env if there is one, for another CF_HTTP_PORT.
-include .env
CONSOLE := http://cloudforge-console.localhost:$(or $(CF_HTTP_PORT),8080)

.PHONY: init up down register cfnode test test-go clean

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

# The api first, because registering the runner needs it. Then everything else, with the runner.
up: init cfnode
	docker compose up --detach --build --wait nginx
	@$(MAKE) --silent register
	docker compose up --detach --build --wait

down:
	docker compose down

# Registers this machine's runner, once: its credential file is the proof. Needs the api (`make up` runs it).
register: secrets/runner_credential

# A one-time token from the dashboard API, which `cfnode register` trades for the runner's credential (R1).
# `| cfnode` is an order-only prerequisite: build the program first, but never register again because it changed.
secrets/runner_credential: | cfnode
	@token=$$(curl --fail --silent --show-error -H 'Content-Type: application/json' -d '{"pool": "trusted"}' \
		$(CONSOLE)/api/orgs/default/runner-tokens | sed -E 's/.*"token":"([^"]+)".*/\1/') \
	&& CF_API_URL=$(CONSOLE) CF_REGISTRATION_TOKEN=$$token cfnode/bin/cfnode register > $@.new \
	&& chmod 444 $@.new && mv -f $@.new $@
	@echo "created $@"

# The node program, built on the host; its image copies it (cfnode/Dockerfile). Go's build cache makes this quick
# when nothing changed. CGO_ENABLED=0 leaves the C library out, so the binary also runs on Alpine.
cfnode:
	cd cfnode && CGO_ENABLED=0 go build -o bin/cfnode ./cmd/cfnode

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
	@printf 'Delete all CloudForge data: database, images, Git repositories? [y/N] ' && read answer && [ "$$answer" = y ]
	docker compose down --volumes
	rm -f secrets/runner_credential  # it belonged to the deleted database
