# Tools run as pinned one-offs rather than entering go.mod, so their
# dependencies never reach the service build.
GOOSE       := go run github.com/pressly/goose/v3/cmd/goose@v3.28.0
SQLC        := go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.1

MIGRATIONS := internal/store/migrations
SEED       := internal/store/seed/0001_one_cinema.sql

export CINEBOOK_DSN ?= postgres://cinebook:cinebook@localhost:5432/cinebook?sslmode=disable

.PHONY: all build test vet lint sqlc sqlc-diff check db-up db-down seed \
	migrate-up migrate-down migrate-status migrate-cycle run loadgen clean

all: check build

build:
	go build -o bin/ ./cmd/...

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	$(STATICCHECK) ./...

sqlc:
	$(SQLC) generate

sqlc-diff:
	$(SQLC) diff

check: vet lint sqlc-diff test

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

seed:
	docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U cinebook -d cinebook < $(SEED)

migrate-up:
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" up

migrate-down:
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" down

migrate-status:
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" status

# Every Down has to undo its Up exactly, or the second up fails.
migrate-cycle:
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" up
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" reset
	$(GOOSE) -dir $(MIGRATIONS) postgres "$$CINEBOOK_DSN" up

run:
	go run ./cmd/cinebook-api

loadgen:
	go run ./cmd/cinebook-loadgen $(LOADGEN_FLAGS)

clean:
	rm -rf bin
