.PHONY: test test-integration build build-app templ fmt vet run-app

test:
	go test ./...

# Nécessite pdftotext/pdftoppm (poppler-utils) sur le PATH.
test-integration:
	go test ./... -tags=integration

build:
	go build -o bin/jarvis ./cmd/jarvis

# Application web unifiée (upload/suivi de documents + navigateur de
# classes/tests) — cf. CLAUDE.md section "Application web unifiée
# (cmd/jarvisapp)". Remplace depuis le jalon 13 les anciens binaires
# séparés cmd/jarvisweb et cmd/codebrowser.
build-app: templ
	go build -o bin/jarvisapp ./cmd/jarvisapp

# Régénère cmd/jarvisapp/templates/*_templ.go à partir des .templ.
# Nécessite `go install github.com/a-h/templ/cmd/templ@latest`.
templ:
	templ generate ./...

# Lance l'application web unifiée en local. Adapter les URLs/modèles/Mongo
# à ton setup (cf. CLAUDE.md, section "Application web unifiée") —
# MONGO_URI doit être exporté dans l'environnement. Les deux serveurs
# llama.cpp (VLM :8080, LLM :8081) doivent déjà tourner.
run-app: build-app
	./bin/jarvisapp \
		--vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 \
		--llm-url http://127.0.0.1:8081/v1 --llm-model qwen3-8b \
		--mongo-uri "$$MONGO_URI" \
		--out-dir ./data/results

fmt:
	gofmt -l .

vet:
	go vet ./...
