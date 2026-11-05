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

// TestRunFails covers specs the generator must refuse instead of emitting a wrong type: a
// constant taking a type's name (renaming either one on the fly would break users of the
// published name), and an allOf of an object and null, which no value satisfies.
func TestRunFails(t *testing.T) {
	const head = `openapi: 3.1.1
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
`
	tests := []struct {
		name, props string
		want        []string
	}{
		{
			name: "constant collides with a type",
			props: `        by: {type: string, enum: [mode]}
        byMode: {type: string, enum: [asc]}
`,
			want: []string{"SortByMode", "constantNames"},
		},
		{
			name: "allOf of object and null",
			props: `        by:
          allOf:
            - type: object
              properties:
                a: {type: string}
            - type: "null"
`,
			want: []string{"allOf of null"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := filepath.Join(t.TempDir(), "spec.yaml")
			if err := os.WriteFile(spec, []byte(head+tt.props), 0o644); err != nil {
				t.Fatal(err)
			}
			err := run(spec, t.TempDir())
			if err == nil {
				t.Fatalf("run succeeded, want an error mentioning %q", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("got %v, want it to mention %q", err, w)
				}
			}
		})
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
