.PHONY: help up down logs ps restart test psql

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n",$$1,$$2}'

up: ## Build and start the dev environment with live reload
	docker compose up --build

down: ## Stop and remove containers
	docker compose down

logs: ## Tail the app logs
	docker compose logs -f app

ps: ## Show running services
	docker compose ps

restart: ## Restart the app container
	docker compose restart app

test: ## Run the Go test suite on the host
	go test ./...

psql: ## Open a psql shell in the db container
	docker compose exec db psql -U $${POSTGRES_USER:-app} -d $${POSTGRES_DB:-app}
