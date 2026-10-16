.PHONY: run test test-mysql clean-mysql build build-gate-mcp

GOCACHE ?= /tmp/agentflow-gocache
# Use the local toolchain; go.mod requires go >= 1.26.9.

run:
	GOCACHE=$(GOCACHE) go run ./cmd/agentflow

test:
	GOCACHE=$(GOCACHE) go test -p=1 ./... -count=1 -timeout=120s

# test-mysql runs the gorm integration tests against a throwaway local MySQL
# container. It never touches any externally configured database: the DSN is
# bound to 127.0.0.1 and the container is removed afterwards.
MYSQL_CONTAINER ?= agentflow-test-mysql
MYSQL_PORT ?= 33306
MYSQL_TEST_DB ?= agentflow_it
MYSQL_IMAGE ?= mysql:8.0

test-mysql:
	@docker rm -f $(MYSQL_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --name $(MYSQL_CONTAINER) -p 127.0.0.1:$(MYSQL_PORT):3306 \
		-e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=$(MYSQL_TEST_DB) $(MYSQL_IMAGE)
	@echo "waiting for MySQL to become ready..."
	@until docker exec $(MYSQL_CONTAINER) mysqladmin ping -uroot -proot --silent >/dev/null 2>&1; do sleep 1; done
	@until docker exec $(MYSQL_CONTAINER) mysql -uroot -proot -e "SELECT 1" >/dev/null 2>&1; do sleep 1; done
	@sleep 3
	AGENTFLOW_TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:$(MYSQL_PORT))/$(MYSQL_TEST_DB)" \
		GOCACHE=$(GOCACHE) go test -p=1 ./internal/repo/ -count=1 -timeout=180s
	$(MAKE) clean-mysql

clean-mysql:
	-@docker rm -f $(MYSQL_CONTAINER) >/dev/null 2>&1 || true

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -o bin/agentflow ./cmd/agentflow
	GOCACHE=$(GOCACHE) go build -o bin/agentflow-mcp ./cmd/agentflow-mcp
	GOCACHE=$(GOCACHE) go build -o bin/agentflow-runner ./cmd/agentflow-runner
	$(MAKE) build-gate-mcp

build-gate-mcp:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 GOCACHE=$(GOCACHE) go build -o bin/agentflow-gate-mcp-darwin-amd64 ./cmd/agentflow-gate-mcp
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 GOCACHE=$(GOCACHE) go build -o bin/agentflow-gate-mcp-darwin-arm64 ./cmd/agentflow-gate-mcp
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOCACHE=$(GOCACHE) go build -o bin/agentflow-gate-mcp-linux-amd64 ./cmd/agentflow-gate-mcp
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOCACHE=$(GOCACHE) go build -o bin/agentflow-gate-mcp-linux-arm64 ./cmd/agentflow-gate-mcp
