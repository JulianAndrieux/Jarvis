# Jarvis hébergé (rôle cloud) — cf. docs/deploiement-cloud.md.
#
# Une seule étape, délibérément : l'image garde le code source et la
# chaîne Go. L'espace Admin (Classes, Modèle, Tests, Architecture) analyse
# le module au démarrage et relit CLAUDE.md et l'historique git — sans
# eux, ces pages n'ont rien à montrer.
FROM golang:1.26-bookworm

# Outils du pipeline : poppler (pdftotext, pdftoppm), LibreOffice (Word,
# Excel, PowerPoint, texte), ImageMagick (images — sips n'existe que sur
# macOS), polices courantes (sinon les PDF convertis changent de mise en
# page), fuseaux horaires (heures affichées en heure locale).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      poppler-utils \
      libreoffice-writer libreoffice-calc libreoffice-impress \
      imagemagick libheif1 \
      fonts-dejavu fonts-liberation fonts-crosextra-carlito fonts-crosextra-caladea \
      ca-certificates tzdata curl \
 # Décodeur HEIC (photos d'iPhone) : un paquet séparé selon la version de
 # Debian, déjà lié à libheif sur les plus anciennes.
 && (apt-get install -y --no-install-recommends libheif-plugin-libde265 || true) \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /usr/local/bin/jarvisapp ./cmd/jarvisapp

# Jamais root. HOME=/data : ~/.jarvis (clé de chiffrement, configuration
# de la boîte mail) vit sur le volume, donc survit à une reconstruction.
RUN useradd --uid 10001 --home-dir /data --create-home jarvis \
 && chown -R jarvis:jarvis /app
USER jarvis
ENV HOME=/data \
    GOPROXY=off \
    GOTOOLCHAIN=local
VOLUME /data

EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8090/health || exit 1

ENTRYPOINT ["jarvisapp", "--module-dir", "/app"]
