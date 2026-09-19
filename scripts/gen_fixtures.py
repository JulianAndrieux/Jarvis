#!/usr/bin/env python3
"""Génère les PDF minimaux de testdata/fixtures/.

Ces PDF sont construits à la main (pas de dépendance externe) pour garder
des fixtures petites, déterministes et faciles à relire. Relancer ce script
régénère les fixtures à l'identique (mêmes octets).

Usage: python3 scripts/gen_fixtures.py
"""
import os
import subprocess
import tempfile
from pathlib import Path

FIXTURES_DIR = Path(__file__).resolve().parent.parent / "testdata" / "fixtures"


def _content_stream(lines_with_y):
    """Construit un flux de contenu PDF avec une ou plusieurs lignes de texte.

    Chaque ligne est positionnée en absolu via `Tm` (text matrix) pour éviter
    tout calcul de décalage relatif entre lignes.
    """
    parts = ["BT", "/F1 18 Tf"]
    for text, y in lines_with_y:
        escaped = text.replace("\\", r"\\").replace("(", r"\(").replace(")", r"\)")
        parts.append(f"1 0 0 1 72 {y} Tm ({escaped}) Tj")
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

    print(f"Fixtures écrites dans {FIXTURES_DIR}")


if __name__ == "__main__":
    main()
