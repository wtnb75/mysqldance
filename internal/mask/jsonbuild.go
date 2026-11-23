package mask

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"mysqldance/internal/sqlvalue"
)

// buildJSONPaths checks the paths of a rule for a JSON column and prepares the
// Func that rewrites the column. It reports every problem it finds, so that the
// file can be fixed in one go: a path that does not parse, two paths of which
// one points inside the other (or at the same value), a transform that cannot
// be used under a path, and what the transform finds wrong with its arguments
// or its salt. What a path reaches is not known until a row is read; a value of
// another type than the transform takes is an error then.
func buildJSONPaths(e Entry, col CatalogColumn, reg *Registry, salt *Salt) (Func, error) {
	var errs []error
	type parsed struct {
		path  string
		steps []pathStep
	}
	var ok []parsed
	changes := false // some path has a transform that changes values
	root := &pathNode{}
	for _, pr := range e.Rule.Paths {
		bad := func(err error) { errs = append(errs, fmt.Errorf("path %q: %w", pr.Path, err)) }
		steps, err := parseJSONPath(pr.Path)
		if err != nil {
			bad(err)
			continue
		}
		if err := checkUnsupported(pr.Rule); err != nil {
			bad(err)
			continue
		}
		if pr.Rule.Domain != "" {
			bad(errors.New("domain is not supported in the rule of a path"))
			continue
		}
		leaf, err := buildPathLeaf(pr, col, reg, salt)
		if err != nil {
			bad(err)
			continue
		}
		if i := slices.IndexFunc(ok, func(p parsed) bool { return pathsOverlap(p.steps, steps) }); i >= 0 {
			bad(fmt.Errorf("it overlaps the path %q: a value would be rewritten twice (one path points to or into what the other does)", ok[i].path))
			continue
		}
		ok = append(ok, parsed{path: pr.Path, steps: steps})
		if leaf == nil {
			continue // keep: the path counts for the overlap check, and nothing else
		}
		if root.path == "" {
			root.path = pr.Path
		}
		root.add(steps, pr.Path, leaf)
		changes = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	rw := &jsonRewriter{root: root}
	return func(in Input) (sqlvalue.Value, error) {
		if !changes || in.Value.IsNull() {
			return in.Value, nil
		}
		out, changed, err := rw.rewrite(in.Value.Bytes())
		if err != nil {
			return sqlvalue.Value{}, err
		}
		if !changed {
			return in.Value, nil
		}
		return sqlvalue.NewValue(in.Value.Type(), out, false), nil
	}, nil
}

// buildPathLeaf prepares the transform of one path. keep leaves the value alone
// and gives no leaf (nil); a transform that has no BuildLeaf cannot be used
// under a path.
func buildPathLeaf(pr PathRule, col CatalogColumn, reg *Registry, salt *Salt) (*pathLeaf, error) {
	t, found := reg.Lookup(pr.Rule.Use)
	if !found {
		return nil, fmt.Errorf("unknown transform %q", pr.Rule.Use)
	}
	if t.NoChange {
		return nil, nil
	}
	if t.BuildLeaf == nil {
		return nil, fmt.Errorf("transform %q cannot be used on a JSON path (usable: %s)", t.Name, strings.Join(leafTransformNames(reg), ", "))
	}
	key, err := keyOf(t, salt, "")
	if err != nil {
		return nil, err
	}
	fn, err := t.BuildLeaf(BuildContext{
		Args: pr.Rule.Args,
		// JSON text is utf8mb4, whatever the column says.
		Column: Column{Name: col.Name, TypeName: "VARCHAR", ColumnType: "text", DataType: "text", CharacterSet: "utf8mb4", Nullable: true, Index: -1},
		Key:    key,
	})
	if err != nil {
		return nil, err
	}
	return &pathLeaf{fn: fn}, nil
}

func leafTransformNames(reg *Registry) []string {
	var names []string
	for _, n := range reg.Names() {
		if t, _ := reg.Lookup(n); t.NoChange || t.BuildLeaf != nil {
			names = append(names, n)
		}
	}
	return names
}
