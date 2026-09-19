#!/usr/bin/env python3
"""Génère les PDF minimaux de testdata/fixtures/.

Ces PDF sont construits à la main (pas de dépendance externe) pour garder
des fixtures petites, déterministes et faciles à relire. Relancer ce script
régénère les fixtures à l'identique (mêmes octets).

Usage: python3 scripts/gen_fixtures.py
"""
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

    # Assemble avec table xref correcte
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

    print(f"Fixtures écrites dans {FIXTURES_DIR}")


if __name__ == "__main__":
    main()
