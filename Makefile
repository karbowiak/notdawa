.PHONY: build tidy db-up db-down migrate import serve test

build:
	go build -o bin/notdawa ./cmd/notdawa

tidy:
	go mod tidy

db-up:
	docker compose up -d
	@echo "waiting for postgres…"; until docker compose exec -T db pg_isready -U notdawa >/dev/null 2>&1; do sleep 1; done; echo "ready."

db-down:
	docker compose down

migrate: build
	./bin/notdawa migrate

import: build
	./bin/notdawa import $(TARGET)

serve: build
	./bin/notdawa serve

test:
	go test ./...
