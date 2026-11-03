HTTP_PORT ?= 8085
DB_URL    ?= postgres://currency:currency@localhost:5442/currency?sslmode=disable
BIN       := bin/currency
TEST      ?= .
PKG       ?= ./internal/service/

.DEFAULT_GOAL := help

.PHONY: help up down reset-db run build docker-up docker-logs test test-race test-one cover \
	fmt vet lint check gen mocks tidy vuln \
	psql updates migrate-version migrate-down migrate-create

help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- Infrastructure and run ---------------------------------------------------

up: ## Start only Postgres in Docker (for make run)
	docker compose up -d --wait postgres

down: ## Stop all containers (data is kept)
	docker compose down

reset-db: ## Stop all containers and delete the data
	docker compose down -v

run: ## Run the service locally (HTTP_PORT=8085 by default)
	HTTP_PORT=$(HTTP_PORT) go run ./cmd/server

build: ## Build the binary into bin/
	go build -o $(BIN) ./cmd/server

docker-up: ## Build the image and run service + Postgres in Docker (APP_PORT=8080 by default)
	docker compose up -d --build --wait

docker-logs: ## Follow the service logs in Docker
	docker compose logs -f app

# --- Tests ----------------------------------------------------------------------

test: ## Run all tests
	go test ./...

test-race: ## Run all tests with the race detector
	go test -race -count=1 ./...

test-one: ## Run one test: make test-one TEST=TestName [PKG=./internal/service/]
	go test -race -count=1 -v -run '$(TEST)' $(PKG)

cover: ## Show test coverage in the browser
	go test -coverprofile=cover.out ./...
	go tool cover -html=cover.out

# --- Code quality ---------------------------------------------------------------

fmt: ## Format code and imports
	gofmt -w .
	goimports -w .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

check: fmt vet lint test-race ## Everything to run before a commit

gen: ## Regenerate API types from api/openapi.yaml
	oapi-codegen -generate types -package api api/openapi.yaml > internal/api/generated.go

mocks: ## Regenerate gomock mocks (go generate, mockgen is a go tool in go.mod)
	go generate ./...

tidy: ## Clean up go.mod and go.sum
	go mod tidy

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# --- Database -------------------------------------------------------------------

psql: ## Open psql in the Postgres container
	docker compose exec postgres psql -U currency -d currency

updates: ## Show the 10 latest quote updates
	docker compose exec postgres psql -U currency -d currency -c \
		'select pair, status, price, source_date, attempts, created_at from quote_updates order by created_at desc limit 10'

migrate-version: ## Show the applied migration version
	migrate -path migrations -database '$(DB_URL)' version

migrate-down: ## Roll back the last migration
	migrate -path migrations -database '$(DB_URL)' down 1

migrate-create: ## Create a migration: make migrate-create NAME=add_something
	@test -n "$(NAME)" || (echo "usage: make migrate-create NAME=add_something" && exit 1)
	migrate create -ext sql -dir migrations -seq $(NAME)
