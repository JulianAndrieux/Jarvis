package codemap

import (
	"fmt"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

// Analyze charge les packages du module en dir correspondant à patterns
// (ex. "./..." pour tout le module) via go/packages, et construit un
// Model : types déclarés, champs, méthodes, embeddings, interfaces
// implémentées. Aucun code du module analysé n'est exécuté — analyse
// statique uniquement (go/types).
func Analyze(dir string, patterns ...string) (*Model, error) {
	cfg := &packages.Config{
		Dir: dir,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedDeps | packages.NeedImports |
			packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedModule,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("codemap: load packages: %w", err)
	}

	model := &Model{}
	// allTypes indexe chaque TypeInfo par (package, nom) pour résoudre les
	// relations (Embeds/Implements et leurs inverses) une fois tous les
	// types de tous les packages demandés collectés.
	allTypes := map[TypeRef]*TypeInfo{}
	allNamed := map[TypeRef]*types.Named{}

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue // erreurs de chargement pour ce package ; on continue avec les autres
		}
		p := &Package{Path: pkg.PkgPath, Name: pkg.Name, ImportNames: importNames(pkg)}
		if model.ModulePath == "" && pkg.Module != nil {
			model.ModulePath = pkg.Module.Path
		}
		sources := collectSources(pkg)
		qualifier := shortQualifier(pkg.Types)

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}

			ti := &TypeInfo{
				Name:     name,
				Package:  pkg.PkgPath,
				Exported: tn.Exported(),
				Kind:     kindOf(named),
			}
			if s, ok := named.Underlying().(*types.Struct); ok {
				ti.Fields = fieldsOf(s, qualifier)
			}
			ti.Methods = declaredMethodsOf(named, qualifier)
			if src, ok := sources.types[name]; ok {
				ti.Source, ti.File, ti.Line = src.text, src.file, src.line
			}
			for i := range ti.Methods {
				if src, ok := sources.methods[name+"."+ti.Methods[i].Name]; ok {
					ti.Methods[i].Source, ti.Methods[i].File, ti.Methods[i].Line = src.text, src.file, src.line
				}
			}

			p.Types = append(p.Types, ti)
			ref := TypeRef{Package: pkg.PkgPath, Name: name}
			allTypes[ref] = ti
			allNamed[ref] = named
		}

		sort.Slice(p.Types, func(i, j int) bool { return p.Types[i].Name < p.Types[j].Name })
		model.Packages = append(model.Packages, p)
	}

	sort.Slice(model.Packages, func(i, j int) bool { return model.Packages[i].Path < model.Packages[j].Path })

	resolveEmbeds(allTypes, allNamed)
	resolveInterfaces(allTypes, allNamed)
	keepKnownFieldRefs(allTypes)

	return model, nil
}

func kindOf(named *types.Named) Kind {
	switch named.Underlying().(type) {
	case *types.Struct:
		return KindStruct
	case *types.Interface:
		return KindInterface
	default:
		return KindOther
	}
}

// shortQualifier écrit les types comme dans le code du package own :
// non qualifiés pour own, "nom.T" pour les autres (jamais le chemin
// d'import complet).
func shortQualifier(own *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == own {
			return ""
		}
		return p.Name()
	}
}

func fieldsOf(s *types.Struct, q types.Qualifier) []FieldInfo {
	fields := make([]FieldInfo, s.NumFields())
	for i := 0; i < s.NumFields(); i++ {
		f := s.Field(i)
		fi := FieldInfo{
			Name:      f.Name(),
			Type:      types.TypeString(f.Type(), q),
			Tag:       s.Tag(i),
			Anonymous: f.Anonymous(),
		}
		t := f.Type()
		if p, ok := t.(*types.Pointer); ok {
			fi.Optional = true
			t = p.Elem()
		}
		switch t.(type) {
		case *types.Slice, *types.Array, *types.Map:
			fi.Many = true
		}
		fi.Refs = namedRefsIn(f.Type())
		fields[i] = fi
	}
	return fields
}

// namedRefsIn retourne les types nommés atteignables dans t (à travers
// pointeurs, tranches, tableaux, maps, canaux et arguments de types
// génériques), sans doublon, dans l'ordre de rencontre.
func namedRefsIn(t types.Type) []TypeRef {
	var refs []TypeRef
	seen := map[TypeRef]bool{}
	var walk func(types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case *types.Pointer:
			walk(t.Elem())
		case *types.Slice:
			walk(t.Elem())
		case *types.Array:
			walk(t.Elem())
		case *types.Map:
			walk(t.Key())
			walk(t.Elem())
		case *types.Chan:
			walk(t.Elem())
		case *types.Named:
			if obj := t.Obj(); obj.Pkg() != nil {
				ref := TypeRef{Package: obj.Pkg().Path(), Name: obj.Name()}
				if !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
			for i := 0; i < t.TypeArgs().Len(); i++ {
				walk(t.TypeArgs().At(i))
			}
		}
	}
	walk(t)
	return refs
}

// keepKnownFieldRefs ne garde, dans les Refs de chaque champ, que les
// types effectivement analysés (types du module) — la bibliothèque
// standard n'a pas sa place dans le modèle de données.
func keepKnownFieldRefs(allTypes map[TypeRef]*TypeInfo) {
	for _, ti := range allTypes {
		for i := range ti.Fields {
			var kept []TypeRef
			for _, r := range ti.Fields[i].Refs {
				if _, ok := allTypes[r]; ok {
					kept = append(kept, r)
				}
			}
			ti.Fields[i].Refs = kept
		}
	}
}

// declaredMethodsOf retourne les méthodes déclarées DIRECTEMENT sur named
// (pas les méthodes promues par embedding — celles-ci se trouvent en
// suivant Embeds jusqu'au type qui les déclare, comme dans un navigateur
// de classes classique).
func declaredMethodsOf(named *types.Named, q types.Qualifier) []MethodInfo {
	// Une interface porte ses méthodes sur son type sous-jacent, pas sur
	// le type nommé (named.NumMethods() vaut 0) : on les lit là.
	if iface, ok := named.Underlying().(*types.Interface); ok {
		methods := make([]MethodInfo, 0, iface.NumMethods())
		for i := 0; i < iface.NumMethods(); i++ {
			fn := iface.Method(i)
			methods = append(methods, MethodInfo{Name: fn.Name(), Signature: types.TypeString(fn.Type(), q)})
		}
		sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
		return methods
	}

	methods := make([]MethodInfo, 0, named.NumMethods())
	for i := 0; i < named.NumMethods(); i++ {
		fn := named.Method(i)
		sig := fn.Type().(*types.Signature)
		pointerRecv := false
		if recv := sig.Recv(); recv != nil {
			if _, ok := recv.Type().(*types.Pointer); ok {
				pointerRecv = true
			}
		}
		methods = append(methods, MethodInfo{
			Name:            fn.Name(),
			Signature:       types.TypeString(sig, q),
			PointerReceiver: pointerRecv,
		})
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
	return methods
}

// resolveEmbeds détecte les champs anonymes de chaque struct (Embeds) et
// construit l'index inversé (EmbeddedBy) — seulement pour les types
// embeddés qui font eux-mêmes partie de allTypes (les types de la
// bibliothèque standard ou hors périmètre ne sont pas suivis).
func resolveEmbeds(allTypes map[TypeRef]*TypeInfo, allNamed map[TypeRef]*types.Named) {
	for ref, named := range allNamed {
		s, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		ti := allTypes[ref]
		for i := 0; i < s.NumFields(); i++ {
			f := s.Field(i)
			if !f.Anonymous() {
				continue
			}
			embedRef, ok := namedRefOf(f.Type())
			if !ok {
				continue
			}
			ti.Embeds = append(ti.Embeds, embedRef)
			if parent, ok := allTypes[embedRef]; ok {
				parent.EmbeddedBy = append(parent.EmbeddedBy, ref)
			}
		}
	}
	for _, ti := range allTypes {
		sortRefs(ti.Embeds)
		sortRefs(ti.EmbeddedBy)
	}
}

// resolveInterfaces calcule, pour chaque paire (type concret, interface)
// connue, si le type concret satisfait l'interface (valeur ou pointeur).
func resolveInterfaces(allTypes map[TypeRef]*TypeInfo, allNamed map[TypeRef]*types.Named) {
	var interfaces []TypeRef
	for ref, named := range allNamed {
		if _, ok := named.Underlying().(*types.Interface); ok {
			interfaces = append(interfaces, ref)
		}
	}

	for implRef, implNamed := range allNamed {
		if _, ok := implNamed.Underlying().(*types.Interface); ok {
			continue // une interface n'"implémente" pas une autre interface ici
		}
		for _, ifaceRef := range interfaces {
			if implRef.Package == ifaceRef.Package && implRef.Name == ifaceRef.Name {
				continue
			}
			ifaceType, ok := allNamed[ifaceRef].Underlying().(*types.Interface)
			if !ok {
				continue
			}
			satisfies := types.Implements(implNamed, ifaceType) ||
				types.Implements(types.NewPointer(implNamed), ifaceType)
			if !satisfies {
				continue
			}
			allTypes[implRef].Implements = append(allTypes[implRef].Implements, ifaceRef)
			allTypes[ifaceRef].ImplementedBy = append(allTypes[ifaceRef].ImplementedBy, implRef)
		}
	}

	for _, ti := range allTypes {
		sortRefs(ti.Implements)
		sortRefs(ti.ImplementedBy)
	}
}

// namedRefOf extrait un TypeRef si t (ou *t) est un type nommé — utilisé
// pour résoudre les champs anonymes (embeds), qui peuvent être une valeur
// ou un pointeur.
func namedRefOf(t types.Type) (TypeRef, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return TypeRef{}, false
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return TypeRef{}, false // type prédéclaré (ex. error)
	}
	return TypeRef{Package: obj.Pkg().Path(), Name: obj.Name()}, true
}

func sortRefs(refs []TypeRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Package != refs[j].Package {
			return refs[i].Package < refs[j].Package
		}
		return refs[i].Name < refs[j].Name
	})
}
