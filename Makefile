# Atalhos de desenvolvimento. Rode `make help` para ver os alvos.
# Variáveis podem ser sobrescritas: make server SERVER_ADDRESS=:9090

SERVER_ADDRESS ?= :8080
SERVER_URL     ?= http://localhost:8080
DATABASE_PATH  ?= ./chat.db
GO_TAGS        ?= webkit2_41
CLIENTS        ?= 100
MESSAGES       ?= 20
LOAD_DB        ?= /tmp/chat-load.db
PPROF_URL      ?= http://localhost:6060

.DEFAULT_GOAL := help
.PHONY: help server test test-back test-front test-load e2e vet fmt loadgen load-server load-reset load-test dev dev-mock build bindings tidy

help: ## Lista os alvos disponíveis
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-12s %s\n", $$1, $$2}'

server: ## Sobe o servidor em modo dev
	cd back-end && DEV_MODE=true SERVER_ADDRESS=$(SERVER_ADDRESS) DATABASE_PATH=$(DATABASE_PATH) go run ./cmd/server

test: test-back test-front test-load ## Roda os testes de todos os módulos

test-back: ## Testes do back-end com detector de corrida
	cd back-end && go test -race ./...

test-front: ## Testes da camada Go do cliente
	cd front-end && go test -race -tags $(GO_TAGS) ./...

test-load: ## Testes unitários do teste de carga
	cd teste-carga && go test -race ./...

e2e: ## Teste ponta a ponta (exige servidor em E2E_SERVER_URL)
	cd front-end && E2E_SERVER_URL=$${E2E_SERVER_URL:-http://localhost:18080} go test -tags '$(GO_TAGS) e2e' -race -run E2E -v .

vet: ## Análise estática de todos os módulos
	cd back-end && go vet ./...
	cd front-end && go vet -tags $(GO_TAGS) ./...
	cd teste-carga && go vet -tags loadtest ./...

fmt: ## Formata o código Go
	cd back-end && gofmt -w .
	cd front-end && gofmt -w .
	cd teste-carga && gofmt -w .

loadgen: ## Teste de carga contra SERVER_URL
	cd back-end && go run ./cmd/loadgen -server $(SERVER_URL) -clients $(CLIENTS) -messages $(MESSAGES)

load-server: ## Servidor para teste de carga (DEV_MODE, pprof em :6060, banco preservado)
	cd back-end && DEV_MODE=true PPROF_ADDRESS=localhost:6060 SERVER_ADDRESS=$(SERVER_ADDRESS) DATABASE_PATH=$(LOAD_DB) go run ./cmd/server

load-reset: ## Apaga o banco do teste de carga (usuários e grupos são recriados)
	rm -f $(LOAD_DB) $(LOAD_DB)-wal $(LOAD_DB)-shm

load-test: ## Teste de carga com relatório em teste-carga/results/
	cd teste-carga && go run . -server $(SERVER_URL) -pprof $(PPROF_URL)

dev: ## Cliente desktop com hot reload
	cd front-end && SERVER_URL=$(SERVER_URL) wails dev

dev-mock: ## Cliente com dados falsos, sem servidor
	cd front-end && VITE_MOCK=true wails dev

build: ## Binário do cliente em front-end/build/bin/
	cd front-end && wails build

bindings: ## Regenera os bindings JS após mudar métodos de App
	cd front-end && wails generate module -tags $(GO_TAGS)

tidy: ## Sincroniza go.mod/go.sum dos dois módulos
	cd back-end && go mod tidy
	cd front-end && go mod tidy
	cd teste-carga && go mod tidy
