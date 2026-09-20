.PHONY: test test-integration build build-app build-launcher package-app templ fmt vet run-app

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

# Lanceur : démarre VLM + LLM + jarvisapp en un geste, ouvre le
# navigateur — cf. CLAUDE.md, jalon 14.
build-launcher:
	go build -o bin/jarvis-launcher ./cmd/jarvis-launcher

# Empaquette jarvis-launcher en .app macOS minimal (juste la structure
# Info.plist standard, aucun outil tiers) pour un raccourci Dock.
# Non signé : premier lancement via clic droit > Ouvrir dans le Finder.
package-app: build-launcher
	rm -rf dist/Jarvis.app
	mkdir -p dist/Jarvis.app/Contents/MacOS
	cp packaging/macos/Info.plist dist/Jarvis.app/Contents/Info.plist
	cp bin/jarvis-launcher dist/Jarvis.app/Contents/MacOS/jarvis-launcher
	@echo "dist/Jarvis.app prêt — glisse-le dans /Applications ou le Dock."

fmt:
	gofmt -l .

vet:
	go vet ./...
