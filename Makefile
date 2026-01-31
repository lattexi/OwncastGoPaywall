.PHONY: all build run dev test clean docker-build docker-up docker-down migrate help

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GORUN=$(GOCMD) run
GOTEST=$(GOCMD) test
GOCLEAN=$(GOCMD) clean
GOMOD=$(GOCMD) mod
BINARY_NAME=server
BINARY_PATH=./cmd/server

# Docker parameters
DOCKER_COMPOSE=docker compose

all: test build

## build: Build the application
build:
	$(GOBUILD) -o $(BINARY_NAME) $(BINARY_PATH)

## run: Run the application
run: build
	./$(BINARY_NAME)

## dev: Run in development mode with hot reload (requires air)
dev:
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "Air not installed. Install with: go install github.com/cosmtrek/air@latest"; \
		$(GORUN) $(BINARY_PATH); \
	fi

## test: Run tests
test:
	$(GOTEST) -v ./...

## test-coverage: Run tests with coverage
test-coverage:
	$(GOTEST) -v -cover -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

## lint: Run linter
lint:
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed. Install from https://golangci-lint.run/"; \
	fi

## clean: Clean build files
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html

## deps: Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

## docker-build: Build Docker images
docker-build:
	$(DOCKER_COMPOSE) build

## docker-up: Start all services
docker-up:
	$(DOCKER_COMPOSE) up -d

## docker-down: Stop all services
docker-down:
	$(DOCKER_COMPOSE) down

## docker-logs: View logs
docker-logs:
	$(DOCKER_COMPOSE) logs -f

## docker-restart: Restart all services
docker-restart: docker-down docker-up

## migrate: Run database migrations (requires psql)
migrate:
	@echo "Running migrations..."
	@if [ -f .env ]; then \
		export $$(cat .env | xargs) && \
		psql "$$DATABASE_URL" -f migrations/001_initial.sql; \
	else \
		echo "No .env file found. Set DATABASE_URL manually."; \
	fi

## create-stream: Create a test stream (usage: make create-stream ADMIN_KEY=your-key)
create-stream:
	@curl -X POST http://localhost:3000/admin/streams \
		-H "Content-Type: application/json" \
		-H "X-Admin-Key: $(ADMIN_KEY)" \
		-d '{"slug":"test-stream","title":"Test Stream","description":"A test stream","price_cents":990,"owncast_url":"http://owncast:8080"}'

## help: Show this help
help:
	@echo "Stream Paywall - Available commands:"
	@echo ""
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
