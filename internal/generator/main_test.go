package main

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the current generator")

// TestGolden generates testdata/spec.yaml, which holds the constructs the real spec may lack
// today (recursive refs, nullable list items, explicit null on untyped fields, ...), so a
// regression there shows up here instead of in a later spec update.
func TestGolden(t *testing.T) {
	out := t.TempDir()
	if err := run(filepath.Join("testdata", "spec.yaml"), out); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden")
	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(golden, os.DirFS(out)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := filepath.Glob(filepath.Join(out, "*.gen.go"))
	want, _ := filepath.Glob(filepath.Join(golden, "*.gen.go"))
	if names(got) != names(want) {
		t.Fatalf("generated files %s, golden files %s (run go test -update)", names(got), names(want))
	}
	for _, path := range want {
		w, _ := os.ReadFile(path)
		g, _ := os.ReadFile(filepath.Join(out, filepath.Base(path)))
		if string(g) != string(w) {
			t.Errorf("%s differs from golden (run go test -update and review the diff)", filepath.Base(path))
		}
	}
}

// TestConstantCollisionFails guards the rule that a constant taking a type's name stops
// generation: renaming either one on the fly would break users of the published name.
func TestConstantCollisionFails(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	err := os.WriteFile(spec, []byte(`openapi: 3.1.1
paths:
  /sorts/get:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Sort"
components:
  schemas:
    Sort:
      type: object
      properties:
        by: {type: string, enum: [mode]}
        byMode: {type: string, enum: [asc]}
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = run(spec, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "SortByMode") || !strings.Contains(err.Error(), "constantNames") {
		t.Fatalf("got %v, want a collision error on SortByMode pointing to constantNames", err)
	}
}

func names(paths []string) string {
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}
