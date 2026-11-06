# Local development (docs/operations/local-setup.md).
SECRETS := secrets/pg_superuser_password secrets/pg_owner_password secrets/pg_app_password

.PHONY: init up down test test-go clean

init: $(SECRETS)

# The folder is private to you; the files are readable inside the containers they are mounted into.
secrets/%:
	@mkdir -p secrets && chmod 700 secrets
	@head -c 32 /dev/urandom | od -An -vtx1 | tr -d ' \n' > $@ && chmod 444 $@
	@echo "created $@"

up: init
	docker compose up --detach --build --wait

down:
	docker compose down

test: init test-go
	docker compose run --rm --build test

# Go runs on the host (Go 1.26): gofmt, vet, then the tests with the race detector.
test-go:
	@cd cfnode && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }
	cd cfnode && go vet ./... && go test -race ./...

clean:
	@printf 'Delete the CloudForge database volume? [y/N] ' && read answer && [ "$$answer" = y ]
	docker compose down --volumes
