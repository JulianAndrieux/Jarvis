#!/usr/bin/env bash
# Génère testdata/formats/ : un fichier par format géré (jalon 25), à partir
# de sources HTML/CSV/texte écrites ici — aucun document personnel. Nécessite
# LibreOffice (soffice) et sips (macOS). Les fichiers générés sont versionnés :
# les tests n'ont pas besoin de ces outils pour les lire.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/testdata/formats"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PROFILE="file://$WORK/lo-profile"
lo() { soffice "-env:UserInstallation=$PROFILE" --headless --norestore "$@" >/dev/null 2>&1; }
# Un .html s'ouvre sinon en « Writer/Web », qui n'a pas d'export Word/PDF.
HTMLIN='--infilter=HTML (StarWriter)'

mkdir -p "$OUT"
cd "$WORK"

cat > facture.html <<'HTML'
<html><head><meta charset="utf-8"><title>Facture</title></head><body>
<h1>Facture n° FAC-2026-0917</h1>
<p><b>Fournisseur :</b> Atelier Dubois SARL, 12 rue des Lilas, 57000 Metz</p>
<p><b>Client :</b> Famille Martin — Date : 17/09/2026</p>
<table border="1"><tr><th>Désignation</th><th>Qté</th><th>PU HT</th><th>Total HT</th></tr>
<tr><td>Rénovation cuisine — pose</td><td>1</td><td>1 250,00 €</td><td>1 250,00 €</td></tr>
<tr><td>Fourniture plan de travail chêne</td><td>2</td><td>340,50 €</td><td>681,00 €</td></tr></table>
<p><b>Total HT :</b> 1 931,00 € — <b>TVA 20 % :</b> 386,20 € — <b>Total TTC : 2 317,20 €</b></p>
</body></html>
HTML

# Relevé bancaire façon export Excel français : points-virgules, Windows-1252.
printf 'Date;Libellé;Catégorie;Montant\n05/09/2026;Loyer septembre;Logement;-1 250,00\n08/09/2026;Salaire;Revenu;3 420,15\n12/09/2026;Courses Cora;Alimentation;-187,43\n' \
  | iconv -f utf-8 -t cp1252 > releve.csv

# Traitement de texte : docx, doc (binaire 97-2003), odt, rtf.
for fmt in docx doc odt rtf; do lo "$HTMLIN" --convert-to "$fmt" --outdir . facture.html; done

# Tableur (xlsx, xls, ods) depuis le CSV Windows-1252 : jeu de caractères 1 (MS-1252) dans
# les options du filtre CSV — 76 (UTF-8) casserait les accents du fichier généré.
lo --infilter="CSV:59,34,1,1" --convert-to xlsx --outdir . releve.csv
lo --infilter="CSV:59,34,1,1" --convert-to xls --outdir . releve.csv
lo --infilter="CSV:59,34,1,1" --convert-to ods --outdir . releve.csv

# Présentation : pptx, ppt, odp, via l'import PDF d'Impress.
lo "$HTMLIN" --convert-to pdf --outdir . facture.html
mv facture.pdf facture-source.pdf
lo --infilter=impress_pdf_import --convert-to pptx --outdir . facture-source.pdf
lo --infilter=impress_pdf_import --convert-to ppt --outdir . facture-source.pdf
lo --infilter=impress_pdf_import --convert-to odp --outdir . facture-source.pdf

# Texte et données.
printf 'Compte rendu de chantier — 18/09/2026\n\nPrésents : M. Dubois (artisan), Mme Martin.\nPoints traités :\n- dalle coulée, séchage 28 jours ;\n- devis complémentaire n° DEV-2026-044 : 480,00 € TTC.\n' > notes.txt
printf '# Liste de courses\n\n- farine\n- œufs\n- crème fraîche\n' > courses.md
printf '{"facture": "FAC-2026-0917", "total_ttc": 2317.20, "devise": "EUR"}\n' > donnees.json
cp facture.html page.html

# Images : jpg, png, heic, tiff (sips, macOS) à partir d'une page rendue.
pdftoppm -png -r 100 -singlefile facture-source.pdf scan
sips -s format jpeg scan.png --out scan.jpg >/dev/null
sips -s format heic scan.png --out scan.heic >/dev/null
sips -s format tiff -s formatOptions lzw scan.png --out scan.tiff >/dev/null  # LZW : 57 Ko au lieu de 2,9 Mo

# E-mail : multipart/alternative + pièce jointe, en-têtes encodés, Latin-1.
PDF_B64="$(base64 < facture-source.pdf | fold -w 76)"
cat > message.eml <<EML
From: =?ISO-8859-1?Q?Atelier_Dubois?= <contact@atelier-dubois.example>
To: Famille Martin <martin@example.org>
Subject: =?UTF-8?B?Vm90cmUgZmFjdHVyZSBGQUMtMjAyNi0wOTE3IOKAlCBzZXB0ZW1icmU=?=
Date: Thu, 17 Sep 2026 10:15:00 +0200
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="MIX"

--MIX
Content-Type: multipart/alternative; boundary="ALT"

--ALT
Content-Type: text/plain; charset=ISO-8859-1
Content-Transfer-Encoding: quoted-printable

Bonjour,

Veuillez trouver ci-joint la facture FAC-2026-0917 d'un montant de 2 317,20 =
=A4 TTC, =E0 r=E9gler avant le 17/10/2026.

Cordialement,
Atelier Dubois
--ALT
Content-Type: text/html; charset=UTF-8

<html><body><p>Bonjour,</p><p>Veuillez trouver ci-joint la facture <b>FAC-2026-0917</b> d'un montant de 2&nbsp;317,20&nbsp;&euro; TTC.</p><img src="https://tracker.example/pixel.gif"></body></html>
--ALT--
--MIX
Content-Type: application/pdf; name="FAC-2026-0917.pdf"
Content-Disposition: attachment; filename="FAC-2026-0917.pdf"
Content-Transfer-Encoding: base64

$PDF_B64
--MIX--
EML
# Le corps QP ci-dessus est en Latin-1 ; =A4 n'y est pas le signe euro
# (c'est en ISO-8859-15) : volontaire, on teste le jeu déclaré.

# Fichier seulement stocké.
printf 'PK\003\004\024\000\000\000\000\000' > archive.zip

cp facture.docx facture.doc facture.odt facture.rtf \
   releve.xlsx releve.xls releve.ods releve.csv \
   facture-source.pptx facture-source.ppt facture-source.odp \
   notes.txt courses.md donnees.json page.html \
   scan.jpg scan.png scan.heic scan.tiff message.eml archive.zip "$OUT/"
cp facture-source.pdf "$OUT/facture.pdf"
for f in facture-source.pptx facture-source.ppt facture-source.odp; do mv "$OUT/$f" "$OUT/presentation.${f##*.}"; done
ls -la "$OUT"
