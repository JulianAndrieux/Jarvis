package notes

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Les boîtes d'une note (layout à la Jupyter) : fonctions pures, sans
// accès au Store.

// BlocksOf : les boîtes d'une note, telles qu'on les affiche. Une note
// écrite avant les boîtes n'a que Body : elle se lit comme une boîte
// unique. Lecture seule — rien n'est persisté ici, la conversion a lieu
// à la première écriture (cf. Service.editBlocks).
func BlocksOf(n Note) []Block {
	if len(n.Blocks) > 0 {
		return n.Blocks
	}
	if n.Body == "" {
		return nil
	}
	return []Block{{ID: "b1", Text: n.Body, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}}
}

// JoinBlocks : le texte de toutes les boîtes, archivées comprises (une
// boîte archivée reste trouvable par la recherche plein texte).
func JoinBlocks(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// BlockTags : l'union triée des tags des boîtes.
func BlockTags(blocks []Block) []string {
	var out []string
	for _, b := range blocks {
		for _, t := range b.Tags {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TaskTitleFrom : le titre de la tâche née d'une boîte — sa première
// ligne non vide, débarrassée de ses marques Markdown.
func TaskTitleFrom(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		for _, prefix := range []string{"- ", "* ", "+ "} {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
		for _, box := range []string{"[ ] ", "[x] ", "[X] "} {
			line = strings.TrimSpace(strings.TrimPrefix(line, box))
		}
		line = strings.TrimSpace(strings.Trim(line, "*"))
		if line != "" {
			return line
		}
	}
	return ""
}

// newBlock : une boîte neuve, identifiant tiré au sort.
func newBlock(text string, now time.Time) (Block, error) {
	id, err := newID()
	if err != nil {
		return Block{}, err
	}
	return Block{ID: id, Text: text, CreatedAt: now, UpdatedAt: now}, nil
}

// indexOfBlock : la position de la boîte, −1 si elle n'y est pas.
func indexOfBlock(blocks []Block, id string) int {
	for i, b := range blocks {
		if b.ID == id {
			return i
		}
	}
	return -1
}
