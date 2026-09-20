// Package codemap analyse statiquement un module Go (via go/packages +
// go/types — aucun code n'est exécuté) et construit un modèle navigable
// des types : champs, méthodes déclarées, embeddings (l'équivalent Go de
// l'héritage, cf. CLAUDE.md jalon 9) et interfaces implémentées. C'est le
// moteur derrière cmd/codebrowser, la page de contrôle "à la Glamorous
// Toolkit" demandée pour naviguer dans l'architecture du code.
package codemap

// TypeRef référence un type par son chemin d'import + son nom, sans
// porter le détail complet — utilisé pour les liens de navigation
// (Embeds, EmbeddedBy, Implements, ImplementedBy) sans dupliquer
// l'information.
type TypeRef struct {
	Package string
	Name    string
}

// FieldInfo décrit un champ de struct.
type FieldInfo struct {
	Name      string
	Type      string
	Tag       string
	Anonymous bool
}

// MethodInfo décrit une méthode déclarée directement sur un type (pas les
// méthodes promues par embedding — celles-ci s'obtiennent en suivant
// Embeds jusqu'au type qui les déclare, comme dans un navigateur de
// classes classique).
type MethodInfo struct {
	Name            string
	Signature       string
	PointerReceiver bool
}

// Kind distingue les types struct/interface/autre (alias, type nommé sur
// un type de base...).
type Kind string

const (
	KindStruct    Kind = "struct"
	KindInterface Kind = "interface"
	KindOther     Kind = "other"
)

// TypeInfo est un type déclaré dans un package, avec ses relations.
type TypeInfo struct {
	Name     string
	Package  string // chemin d'import
	Kind     Kind
	Exported bool

	Fields  []FieldInfo  // structs uniquement
	Methods []MethodInfo // méthodes déclarées directement sur ce type

	// Embeds : champs anonymes de ce struct — l'équivalent Go de
	// "superclasses". EmbeddedBy : l'inverse, calculé après coup —
	// l'équivalent de "sous-classes".
	Embeds     []TypeRef
	EmbeddedBy []TypeRef

	// Implements : interfaces que ce type concret satisfait.
	// ImplementedBy : pour une interface, les types concrets qui la
	// satisfont.
	Implements    []TypeRef
	ImplementedBy []TypeRef
}

// Package regroupe les types déclarés dans un package Go du module.
type Package struct {
	Path  string // chemin d'import complet
	Name  string // nom du package tel que déclaré
	Types []*TypeInfo
}

// Model est le résultat complet d'une analyse : tous les packages du
// module, avec leurs types et relations résolues.
type Model struct {
	Packages []*Package
}

// Package retourne le package path, s'il a été analysé.
func (m *Model) Package(path string) (*Package, bool) {
	for _, p := range m.Packages {
		if p.Path == path {
			return p, true
		}
	}
	return nil, false
}

// Type retourne le type name du package pkgPath, s'il existe.
func (m *Model) Type(pkgPath, name string) (*TypeInfo, bool) {
	p, ok := m.Package(pkgPath)
	if !ok {
		return nil, false
	}
	for _, t := range p.Types {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}
