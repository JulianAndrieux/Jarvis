.PHONY: test test-integration build build-web build-codebrowser templ fmt vet run-web run-codebrowser

test:
	go test ./...

# Nécessite pdftotext/pdftoppm (poppler-utils) sur le PATH.
test-integration:
	go test ./... -tags=integration

build:
	go build -o bin/jarvis ./cmd/jarvis

build-web: templ
	go build -o bin/jarvisweb ./cmd/jarvisweb

# Page de contrôle locale (navigateur de classes + tests) — outil de
# développement, cf. CLAUDE.md section "Atelier de code (cmd/codebrowser)".
build-codebrowser: templ
	go build -o bin/codebrowser ./cmd/codebrowser

# Régénère cmd/jarvisweb/templates/*_templ.go et
# cmd/codebrowser/templates/*_templ.go à partir des .templ.
# Nécessite `go install github.com/a-h/templ/cmd/templ@latest`.
templ:
	templ generate ./...

# Lance le serveur web en local. Adapter les URLs/modèles/Mongo à ton
# setup (cf. CLAUDE.md, section "Interface web") — MONGO_URI doit être
# exporté dans l'environnement.
run-web: build-web
	./bin/jarvisweb \
		--vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 \
		--llm-url http://127.0.0.1:8081/v1 --llm-model qwen3-8b \
		--mongo-uri "$$MONGO_URI" \
		--out-dir ./data/results

# Lance la page de contrôle locale en local. --module-dir vide = répertoire
# courant. Aucun modèle VLM/LLM ni MongoDB requis — pure analyse statique.
run-codebrowser: build-codebrowser
	./bin/codebrowser --addr 127.0.0.1:8091

fmt:
	gofmt -l .

vet:
	go vet ./...
