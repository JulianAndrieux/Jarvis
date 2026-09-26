package notes

import (
	"context"
	"fmt"
	"time"
)

// Les opérations de l'interface sur les boîtes d'une note.

// editBlocks charge la note, normalise ses boîtes (BlocksOf : une note
// d'avant les boîtes devient une boîte), applique fn, puis réécrit
// Blocks, Body (dérivé) et UpdatedAt. Seul endroit qui écrit les boîtes
// — Body en dépend, un second chemin le laisserait décrocher.
func (s *Service) editBlocks(ctx context.Context, noteID string, fn func([]Block) ([]Block, error)) (Note, error) {
	n, err := s.note(ctx, noteID)
	if err != nil {
		return Note{}, err
	}
	blocks, err := fn(BlocksOf(n))
	if err != nil {
		return Note{}, err
	}
	n.Blocks, n.Body, n.UpdatedAt = blocks, JoinBlocks(blocks), s.now()
	return n, s.Store.UpdateNote(ctx, n)
}

// block : la boîte blockID de la note, avec sa position.
func block(blocks []Block, blockID string) (int, error) {
	i := indexOfBlock(blocks, blockID)
	if i < 0 {
		return 0, fmt.Errorf("boîte %s introuvable", blockID)
	}
	return i, nil
}

// AddBlock insère une boîte vide après afterID ("" ou boîte inconnue :
// à la fin).
func (s *Service) AddBlock(ctx context.Context, noteID, afterID string) (Block, error) {
	b, err := newBlock("", s.now())
	if err != nil {
		return Block{}, err
	}
	_, err = s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		at := len(blocks)
		if i := indexOfBlock(blocks, afterID); i >= 0 {
			at = i + 1
		}
		out := make([]Block, 0, len(blocks)+1)
		out = append(out, blocks[:at]...)
		out = append(out, b)
		return append(out, blocks[at:]...), nil
	})
	if err != nil {
		return Block{}, err
	}
	return b, nil
}

// SaveBlock enregistre le texte et les tags d'une boîte. tags : séparés
// par des virgules (doublons et vides retirés).
func (s *Service) SaveBlock(ctx context.Context, noteID, blockID, text, tags string) (Block, error) {
	var saved Block
	_, err := s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		i, err := block(blocks, blockID)
		if err != nil {
			return nil, err
		}
		blocks[i].Text, blocks[i].Tags, blocks[i].UpdatedAt = text, splitTags(tags), s.now()
		saved = blocks[i]
		return blocks, nil
	})
	return saved, err
}

// MoveBlock échange la boîte avec sa voisine ; aux bornes, ne fait rien.
func (s *Service) MoveBlock(ctx context.Context, noteID, blockID string, up bool) error {
	_, err := s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		i, err := block(blocks, blockID)
		if err != nil {
			return nil, err
		}
		j := i + 1
		if up {
			j = i - 1
		}
		if j >= 0 && j < len(blocks) {
			blocks[i], blocks[j] = blocks[j], blocks[i]
		}
		return blocks, nil
	})
	return err
}

// DeleteBlock supprime une boîte. Une note a toujours une boîte :
// supprimer la dernière en laisse une vide.
func (s *Service) DeleteBlock(ctx context.Context, noteID, blockID string) error {
	empty, err := newBlock("", s.now())
	if err != nil {
		return err
	}
	_, err = s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		i, err := block(blocks, blockID)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks[:i], blocks[i+1:]...)
		if len(blocks) == 0 {
			blocks = []Block{empty}
		}
		return blocks, nil
	})
	return err
}

// BlockToTask crée une tâche depuis une boîte (saisie rapide sur sa
// première ligne, cf. ParseQuickAdd) et la mémorise sur la boîte. La
// boîte est gardée : la note reste la trace.
func (s *Service) BlockToTask(ctx context.Context, noteID, blockID string) (Task, error) {
	n, err := s.note(ctx, noteID)
	if err != nil {
		return Task{}, err
	}
	blocks := BlocksOf(n)
	i, err := block(blocks, blockID)
	if err != nil {
		return Task{}, err
	}
	title := TaskTitleFrom(blocks[i].Text)
	if title == "" {
		return Task{}, fmt.Errorf("boîte vide : rien à transformer en tâche")
	}
	if id := blocks[i].TaskID; id != "" {
		if _, ok, err := s.Store.GetTask(ctx, id); err != nil {
			return Task{}, err
		} else if ok {
			return Task{}, fmt.Errorf("cette boîte a déjà une tâche")
		}
		// Tâche supprimée entre-temps : la boîte peut en refaire une.
	}
	task, err := s.AddTask(ctx, title, noteID, "")
	if err != nil {
		return Task{}, err
	}
	_, err = s.editBlocks(ctx, noteID, func(bs []Block) ([]Block, error) {
		j, err := block(bs, blockID)
		if err != nil {
			return nil, err
		}
		bs[j].TaskID = task.ID
		return bs, nil
	})
	return task, err
}

// ArchiveBlock archive une boîte en la taguant : au moins un tag est
// exigé (ceux donnés remplacent les siens ; sans tag donné, les siens
// suffisent s'il y en a).
func (s *Service) ArchiveBlock(ctx context.Context, noteID, blockID, tags string) (Block, error) {
	var saved Block
	_, err := s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		i, err := block(blocks, blockID)
		if err != nil {
			return nil, err
		}
		if t := splitTags(tags); len(t) > 0 {
			blocks[i].Tags = t
		}
		if len(blocks[i].Tags) == 0 {
			return nil, fmt.Errorf("pour archiver une boîte, donne-lui au moins un tag")
		}
		now := s.now()
		blocks[i].Archived, blocks[i].ArchivedAt, blocks[i].UpdatedAt = true, now, now
		saved = blocks[i]
		return blocks, nil
	})
	return saved, err
}

// UnarchiveBlock remet une boîte archivée en place ; ses tags restent.
func (s *Service) UnarchiveBlock(ctx context.Context, noteID, blockID string) (Block, error) {
	var saved Block
	_, err := s.editBlocks(ctx, noteID, func(blocks []Block) ([]Block, error) {
		i, err := block(blocks, blockID)
		if err != nil {
			return nil, err
		}
		blocks[i].Archived, blocks[i].ArchivedAt, blocks[i].UpdatedAt = false, time.Time{}, s.now()
		saved = blocks[i]
		return blocks, nil
	})
	return saved, err
}
