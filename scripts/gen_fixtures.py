#!/usr/bin/env python3
"""Génère les PDF minimaux de testdata/fixtures/.

Ces PDF sont construits à la main (pas de dépendance externe) pour garder
des fixtures petites, déterministes et faciles à relire. Relancer ce script
régénère les fixtures à l'identique (mêmes octets).

Usage: python3 scripts/gen_fixtures.py
"""
import math
import os
import subprocess
import tempfile
from pathlib import Path

FIXTURES_DIR = Path(__file__).resolve().parent.parent / "testdata" / "fixtures"


def _escape(text):
    return text.replace("\\", r"\\").replace("(", r"\(").replace(")", r"\)")


def _content_stream(lines_with_y):
    """Construit un flux de contenu PDF avec une ou plusieurs lignes de texte,
    toutes à x=72 en taille 18 (fixtures simples d'origine).

    Chaque ligne est positionnée en absolu via `Tm` (text matrix) pour éviter
    tout calcul de décalage relatif entre lignes.
    """
    parts = ["BT", "/F1 18 Tf"]
    for text, y in lines_with_y:
        parts.append(f"1 0 0 1 72 {y} Tm ({_escape(text)}) Tj")
    parts.append("ET")
    return "\n".join(parts).encode("latin-1")


def _rich_content_stream(text_items, rules=None):
    """Flux de contenu plus riche pour les fixtures "réalistes" : texte
    positionné librement (x, y, taille) + traits (bordures de tableau).

    text_items: liste de (text, x, y, size).
    rules: liste de segments (x1, y1, x2, y2) tracés avant le texte (les
    opérateurs de tracé ne peuvent pas être imbriqués dans un bloc BT/ET).
    """
    parts = []
    if rules:
        parts.append("0.6 w")
        for x1, y1, x2, y2 in rules:
            parts.append(f"{x1} {y1} m {x2} {y2} l S")

    parts.append("BT")
    current_size = None
    for text, x, y, size in text_items:
        if size != current_size:
            parts.append(f"/F1 {size} Tf")
            current_size = size
        parts.append(f"1 0 0 1 {x} {y} Tm ({_escape(text)}) Tj")
    parts.append("ET")
    return "\n".join(parts).encode("latin-1")


def _page_object_stream(content_bytes):
    return b"<< /Length %d >>\nstream\n" % len(content_bytes) + content_bytes + b"\nendstream"


def _assemble(objects):
    """Sérialise une liste d'objets PDF (index 0 = placeholder non utilisé)
    en un PDF complet, avec une table xref correcte."""
    out = bytearray()
    out += b"%PDF-1.4\n"
    offsets = [0] * len(objects)  # offsets[0] inutilisé

    for idx in range(1, len(objects)):
        offsets[idx] = len(out)
        out += f"{idx} 0 obj\n".encode("latin-1")
        out += objects[idx]
        out += b"\nendobj\n"

    xref_offset = len(out)
    n_obj = len(objects)  # inclut l'objet 0 fictif
    out += f"xref\n0 {n_obj}\n".encode("latin-1")
    out += b"0000000000 65535 f \n"
    for idx in range(1, n_obj):
        out += f"{offsets[idx]:010d} 00000 n \n".encode("latin-1")

    out += b"trailer\n"
    out += f"<< /Size {n_obj} /Root 1 0 R >>\n".encode("latin-1")
    out += b"startxref\n"
    out += f"{xref_offset}\n".encode("latin-1")
    out += b"%%EOF"

    return bytes(out)


def build_pdf(pages_text):
    """pages_text: liste de listes de (texte, y) par page. Liste vide => page blanche."""
    objects = []  # 1-indexed via append order; index 0 unused

    n_pages = len(pages_text)
    page_obj_nums = list(range(3, 3 + n_pages))
    content_obj_nums = list(range(3 + n_pages, 3 + 2 * n_pages))
    font_obj_num = 3 + 2 * n_pages

    objects.append(None)  # placeholder pour obj 0 (non utilisé)

    # obj 1: Catalog
    objects.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    # obj 2: Pages
    kids = " ".join(f"{n} 0 R" for n in page_obj_nums)
    objects.append(f"<< /Type /Pages /Kids [{kids}] /Count {n_pages} >>".encode("latin-1"))

    # obj 3..: pages
    for i, page_lines in enumerate(pages_text):
        content_num = content_obj_nums[i]
        page_body = (
            f"<< /Type /Page /Parent 2 0 R "
            f"/Resources << /Font << /F1 {font_obj_num} 0 R >> >> "
            f"/MediaBox [0 0 612 792] /Contents {content_num} 0 R >>"
        )
        objects.append(page_body.encode("latin-1"))

    # content streams
    for page_lines in pages_text:
        content_bytes = _content_stream(page_lines) if page_lines else b""
        objects.append(_page_object_stream(content_bytes))

    # font
    objects.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

    return _assemble(objects)


def build_rich_pdf(pages, page_width=612, page_height=792):
    """Comme build_pdf, mais chaque page est un dict {"text": [(text,x,y,
    size),...], "rules": [(x1,y1,x2,y2),...]} (ou None/vide pour une page
    blanche) — permet mise en page libre (tableaux, en-têtes) pour les
    fixtures réalistes."""
    objects = []

    n_pages = len(pages)
    page_obj_nums = list(range(3, 3 + n_pages))
    content_obj_nums = list(range(3 + n_pages, 3 + 2 * n_pages))
    font_obj_num = 3 + 2 * n_pages

    objects.append(None)
    objects.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    kids = " ".join(f"{n} 0 R" for n in page_obj_nums)
    objects.append(f"<< /Type /Pages /Kids [{kids}] /Count {n_pages} >>".encode("latin-1"))

    for i in range(n_pages):
        content_num = content_obj_nums[i]
        page_body = (
            f"<< /Type /Page /Parent 2 0 R "
            f"/Resources << /Font << /F1 {font_obj_num} 0 R >> >> "
            f"/MediaBox [0 0 {page_width} {page_height}] /Contents {content_num} 0 R >>"
        )
        objects.append(page_body.encode("latin-1"))

    for page in pages:
        if not page:
            content_bytes = b""
        else:
            content_bytes = _rich_content_stream(page.get("text", []), page.get("rules"))
        objects.append(_page_object_stream(content_bytes))

    objects.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

    return _assemble(objects)


def _rotated_placement_matrix(page_width, page_height, angle_deg):
    """Matrice `cm` qui place une image XObject (carré unité) à la taille
    de la page puis la fait pivoter légèrement autour du centre — simule
    un scan pas parfaitement droit plutôt qu'une image plaquée au pixel
    près. Renvoie (a, b, c, d, e, f) pour `a b c d e f cm`."""
    theta = math.radians(angle_deg)
    cos_t, sin_t = math.cos(theta), math.sin(theta)
    cx, cy = page_width / 2, page_height / 2

    a = cos_t * page_width
    b = sin_t * page_width
    c = -sin_t * page_height
    d = cos_t * page_height
    e = cx - cos_t * cx + sin_t * cy
    f = cy - sin_t * cx - cos_t * cy
    return a, b, c, d, e, f


def build_rotated_image_pdf(jpeg_bytes, img_w, img_h, angle_deg, page_width=612, page_height=792):
    """Comme build_image_pdf, mais l'image est légèrement pivotée dans la
    page (simule un scan mal aligné) plutôt que plaquée parfaitement."""
    objects = [None]
    objects.append(b"<< /Type /Catalog /Pages 2 0 R >>")
    objects.append(b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
    objects.append((
        f"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im0 4 0 R >> >> "
        f"/MediaBox [0 0 {page_width} {page_height}] /Contents 5 0 R >>"
    ).encode("latin-1"))
    image_dict = (
        f"<< /Type /XObject /Subtype /Image /Width {img_w} /Height {img_h} "
        f"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode "
        f"/Length {len(jpeg_bytes)} >>\nstream\n"
    ).encode("latin-1")
    objects.append(image_dict + jpeg_bytes + b"\nendstream")

    a, b, c, d, e, f = _rotated_placement_matrix(page_width, page_height, angle_deg)
    content = f"q {a:.6f} {b:.6f} {c:.6f} {d:.6f} {e:.6f} {f:.6f} cm /Im0 Do Q".encode("latin-1")
    objects.append(_page_object_stream(content))

    return _assemble(objects)


def build_image_pdf(jpeg_bytes, img_w, img_h, page_width=612, page_height=792):
    """Construit un PDF d'une page dont le seul contenu est une image JPEG
    (filtre DCTDecode = les octets JPEG bruts, sans ré-encodage), et aucun
    texte natif. Simule une page scannée réaliste (par opposition à
    scanned.pdf, qui est une page vide — utile pour la détection de triage,
    mais dégénérée si on veut vraiment exercer le VLM avec du contenu)."""
    objects = [None]
    objects.append(b"<< /Type /Catalog /Pages 2 0 R >>")  # 1
    objects.append(b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>")  # 2
    objects.append((
        f"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im0 4 0 R >> >> "
        f"/MediaBox [0 0 {page_width} {page_height}] /Contents 5 0 R >>"
    ).encode("latin-1"))  # 3
    image_dict = (
        f"<< /Type /XObject /Subtype /Image /Width {img_w} /Height {img_h} "
        f"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode "
        f"/Length {len(jpeg_bytes)} >>\nstream\n"
    ).encode("latin-1")
    objects.append(image_dict + jpeg_bytes + b"\nendstream")  # 4
    # cm met à l'échelle le carré unité (dans lequel Do dessine toujours
    # l'image) aux dimensions de la page.
    content = f"q {page_width} 0 0 {page_height} 0 0 cm /Im0 Do Q".encode("latin-1")
    objects.append(_page_object_stream(content))  # 5

    return _assemble(objects)


def _render_page_as_jpeg(pdf_path, page, dpi=150):
    """Rend une page d'un PDF en JPEG via pdftoppm + sips (macOS). Renvoie
    (jpeg_bytes, width_px, height_px). Dépendance macOS acceptée ici : ce
    script de génération de fixtures n'a pas besoin d'être portable, seul
    le pipeline jarvis (Go) l'est."""
    with tempfile.TemporaryDirectory() as tmp:
        prefix = os.path.join(tmp, "page")
        subprocess.run(
            ["pdftoppm", "-png", "-r", str(dpi), "-f", str(page), "-l", str(page),
             "-singlefile", pdf_path, prefix],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        png_path = prefix + ".png"
        jpg_path = prefix + ".jpg"
        subprocess.run(
            ["sips", "-s", "format", "jpeg", png_path, "--out", jpg_path],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        def _sips_dim(key):
            out = subprocess.run(["sips", "-g", key, jpg_path], check=True,
                                  capture_output=True, text=True).stdout
            return int(out.strip().splitlines()[-1].split(":")[-1].strip())

        width, height = _sips_dim("pixelWidth"), _sips_dim("pixelHeight")
        with open(jpg_path, "rb") as f:
            jpeg_bytes = f.read()
        return jpeg_bytes, width, height


def main():
    FIXTURES_DIR.mkdir(parents=True, exist_ok=True)

    # 1. native.pdf : une page, texte natif extractible.
    native = build_pdf([
        [("Facture n. 2026-0042", 720), ("Fournisseur: Acme SARL", 700),
         ("Total: 123.45 EUR", 680)],
    ])
    (FIXTURES_DIR / "native.pdf").write_bytes(native)

    # 2. scanned.pdf : une page sans aucun contenu texte (simule une page
    #    image-only ; le rendu image lui-même n'est pas nécessaire pour
    #    tester l'étage de triage, qui ne regarde que le texte extractible).
    scanned = build_pdf([[]])
    (FIXTURES_DIR / "scanned.pdf").write_bytes(scanned)

    # 3. mixed.pdf : deux pages, une avec texte, une sans.
    mixed = build_pdf([
        [("Correspondance", 720), ("Cher client, ceci est un courrier de test.", 700)],
        [],
    ])
    (FIXTURES_DIR / "mixed.pdf").write_bytes(mixed)

    # 4. scanned_content.pdf : une page "scannée" réaliste — image JPEG du
    #    rendu de native.pdf (donc avec du vrai contenu à transcrire),
    #    aucun texte natif. Sert à valider l'étage Parsing (VLM) avec un
    #    contenu qui a un point d'arrêt naturel, contrairement à
    #    scanned.pdf (page vide, utile pour le triage mais dégénère si on
    #    demande au VLM de la décrire).
    jpeg_bytes, img_w, img_h = _render_page_as_jpeg(str(FIXTURES_DIR / "native.pdf"), page=1)
    scanned_content = build_image_pdf(jpeg_bytes, img_w, img_h)
    (FIXTURES_DIR / "scanned_content.pdf").write_bytes(scanned_content)

    # --- Corpus "réaliste" : factures synthétiques plus denses, pensées
    # pour stresser le pipeline au-delà des fixtures minimales ci-dessus
    # (mise en page proche d'une vraie facture, tableau de lignes, totaux
    # ambigus, formats numériques, document multi-pages, scan dégradé).
    # Toujours 100% synthétique — aucune vraie facture d'aucune entreprise.

    # 5. facture_multiligne.pdf : une page dense, tableau de lignes,
    #    plusieurs montants ("Total HT", "TVA", "Total TTC") proches les
    #    uns des autres — teste si l'extraction retrouve le bon total au
    #    milieu du bruit, pas juste dans un texte de 3 lignes.
    multiligne_table_rules = [
        (72, 615, 540, 615),   # sous l'en-tête du tableau
        (72, 547, 540, 547),   # sous la dernière ligne
        (310, 547, 310, 630),  # séparateurs de colonnes
        (365, 547, 365, 630),
        (440, 547, 440, 630),
    ]
    multiligne_text = [
        ("ACME FOURNITURES SARL", 72, 740, 16),
        ("12 rue de la Republique, 75001 Paris", 72, 724, 9),
        ("SIRET: 123 456 789 00012", 72, 712, 9),
        ("Facture n. 2026-0138", 72, 690, 13),
        ("Date: 14/03/2026", 72, 674, 10),
        ("Client: Dupont Industries", 72, 658, 10),
        ("Description", 76, 620, 10),
        ("Qte", 314, 620, 10),
        ("PU HT", 369, 620, 10),
        ("Total HT", 444, 620, 10),
        ("Ramette papier A4", 76, 600, 9),
        ("10", 320, 600, 9),
        ("4.50", 369, 600, 9),
        ("45.00", 444, 600, 9),
        ("Cartouche encre noire", 76, 584, 9),
        ("3", 320, 584, 9),
        ("22.90", 369, 584, 9),
        ("68.70", 444, 584, 9),
        ("Classeurs A4", 76, 568, 9),
        ("20", 320, 568, 9),
        ("1.80", 369, 568, 9),
        ("36.00", 444, 568, 9),
        ("Agrafeuse professionnelle", 76, 552, 9),
        ("2", 320, 552, 9),
        ("15.00", 369, 552, 9),
        ("30.00", 444, 552, 9),
        ("Total HT: 179.70 EUR", 380, 520, 10),
        ("TVA (20%): 35.94 EUR", 380, 505, 10),
        ("Total TTC: 215.64 EUR", 380, 488, 12),
        ("Paiement a 30 jours - IBAN FR76 3000 4000 0112 3456 7890 143", 72, 100, 8),
    ]
    multiligne = build_rich_pdf([{"text": multiligne_text, "rules": multiligne_table_rules}])
    (FIXTURES_DIR / "facture_multiligne.pdf").write_bytes(multiligne)

    # 6. facture_multipage.pdf : 2 pages. Le numéro/fournisseur sont page 1,
    #    le total est page 2, rien ne les relie sur la même page — expose
    #    une vraie limite architecturale de la granularité "un JSON par
    #    page" (décision jalon 1) sur un document où les champs sont
    #    répartis entre plusieurs pages. Documenté dans CLAUDE.md, pas une
    #    fixture "piège" gratuite.
    page1_text = [
        ("GLOBAL TECH DISTRIBUTION", 72, 740, 16),
        ("8 avenue des Champs, 69002 Lyon", 72, 724, 9),
        ("Facture n. 2026-0271", 72, 690, 13),
        ("Date: 02/04/2026", 72, 674, 10),
        ("Client: Martin & Associes", 72, 658, 10),
        ("Detail des articles en page 2.", 72, 600, 10),
    ]
    page2_rules = [
        (72, 695, 540, 695),
        (310, 610, 310, 695),
        (365, 610, 365, 695),
        (440, 610, 440, 695),
    ]
    page2_text = [
        ("Suite de la facture - detail des articles", 72, 740, 12),
        ("Description", 76, 700, 10),
        ("Qte", 314, 700, 10),
        ("PU HT", 369, 700, 10),
        ("Total HT", 444, 700, 10),
        ("Ordinateur portable 14 pouces", 76, 680, 9),
        ("5", 320, 680, 9),
        ("620.00", 369, 680, 9),
        ("3100.00", 444, 680, 9),
        ("Souris sans fil", 76, 664, 9),
        ("5", 320, 664, 9),
        ("18.00", 369, 664, 9),
        ("90.00", 444, 664, 9),
        ("Sacoche de transport", 76, 648, 9),
        ("5", 320, 648, 9),
        ("25.00", 369, 648, 9),
        ("125.00", 444, 648, 9),
        ("Total HT: 3315.00 EUR", 380, 590, 10),
        ("TVA (20%): 663.00 EUR", 380, 575, 10),
        ("Total TTC: 3978.00 EUR", 380, 558, 12),
    ]
    multipage = build_rich_pdf([
        {"text": page1_text},
        {"text": page2_text, "rules": page2_rules},
    ])
    (FIXTURES_DIR / "facture_multipage.pdf").write_bytes(multipage)

    # 7. facture_scannee_realiste.pdf : rendu image de facture_multiligne,
    #    légèrement pivoté (scan pas parfaitement droit) — plus proche
    #    d'un vrai scan que scanned_content.pdf (contenu trivial, aligné
    #    au pixel près).
    ml_jpeg, ml_w, ml_h = _render_page_as_jpeg(str(FIXTURES_DIR / "facture_multiligne.pdf"), page=1)
    scanned_realiste = build_rotated_image_pdf(ml_jpeg, ml_w, ml_h, angle_deg=2.5)
    (FIXTURES_DIR / "facture_scannee_realiste.pdf").write_bytes(scanned_realiste)

    # 8. facture_ambigue.pdf : plusieurs montants proches (sous-total,
    #    remise, base TVA, TVA, total TTC) — teste si l'extraction retourne
    #    bien le Total TTC et pas un des montants intermédiaires.
    ambigue_text = [
        ("Fournitures Bureau Plus", 72, 740, 15),
        ("Facture n. 2026-0055", 72, 710, 13),
        ("Client: SCI Lumiere", 72, 690, 10),
        ("Sous-total: 500.00 EUR", 72, 650, 11),
        ("Remise commerciale: -25.00 EUR", 72, 630, 11),
        ("Base TVA: 475.00 EUR", 72, 610, 11),
        ("TVA (20%): 95.00 EUR", 72, 590, 11),
        ("Total TTC: 570.00 EUR", 72, 560, 13),
    ]
    ambigue = build_rich_pdf([{"text": ambigue_text}])
    (FIXTURES_DIR / "facture_ambigue.pdf").write_bytes(ambigue)

    # 9. facture_format_europeen.pdf : montant au format "1 234,56 EUR"
    #    (espace = séparateur de milliers, virgule = décimale) — teste le
    #    parsing numérique, un point faible connu des LLM.
    format_europeen_text = [
        ("Menuiserie du Nord", 72, 740, 15),
        ("Facture n. 2026-0199", 72, 710, 13),
        ("Client: Mairie de Roubaix", 72, 690, 10),
        ("Total TTC: 1 234,56 EUR", 72, 650, 12),
    ]
    format_europeen = build_rich_pdf([{"text": format_europeen_text}])
    (FIXTURES_DIR / "facture_format_europeen.pdf").write_bytes(format_europeen)

    print(f"Fixtures écrites dans {FIXTURES_DIR}")


if __name__ == "__main__":
    main()
