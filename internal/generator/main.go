// Command generator writes the generated files of the clickmax package from the OpenAPI spec.
//
//	go run -C internal/generator . -spec openapi.yaml -out ../..
//
// The spec lives in this nested module so it stays out of the zip users download.
//
// It fails instead of guessing when the spec uses a construct it cannot map or when two spec names
// map to the same Go identifier, so a spec change never drops or renames an operation silently.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	spec := flag.String("spec", "", "path to openapi.yaml")
	out := flag.String("out", "", "directory of the clickmax package")
	flag.Parse()
	if *spec == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*spec, *out); err != nil {
		fmt.Fprintln(os.Stderr, "generator:", err)
		os.Exit(1)
	}
}

func run(specPath, outDir string) error {
	doc, err := loadSpec(specPath)
	if err != nil {
		return err
	}
	reserved, err := runtimeNames(outDir)
	if err != nil {
		return err
	}
	m, err := build(doc, reserved)
	if err != nil {
		return err
	}
	files, err := render(m)
	if err != nil {
		return err
	}
	stale, err := filepath.Glob(filepath.Join(outDir, "*.gen.go"))
	if err != nil {
		return err
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := os.WriteFile(filepath.Join(outDir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runtimeNames returns the package-level identifiers of the hand-written files in dir, mapped to
// the file declaring them, so a generated type can never redeclare one.
func runtimeNames(dir string) (map[string]string, error) {
	names := map[string]string{}
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".gen.go") || strings.HasSuffix(base, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = base
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = base
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names[n.Name] = base
						}
					}
				}
			}
		}
	}
	return names, nil
}
