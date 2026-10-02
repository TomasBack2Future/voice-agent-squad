package reviewer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestOmittedDeclarationEvidenceContract(t *testing.T) {
	changedHunk := "@@ -10 +10 @@ func f()\n- _ = 0\n+ _ = r\n"
	if strings.Contains(changedHunk, "r:=") {
		t.Fatal("fixture accidentally supplies declaration")
	}
	// The changed hunk is identical in both scopes: only unchanged context
	// distinguishes a valid reference from an actually undeclared symbol.
	scopes := []struct {
		name, source string
		fails        bool
	}{
		{"omitted unchanged declaration", "package p\nfunc f(){\n r:=1\n _=0\n _=0\n _=0\n _=0\n _=0\n _=0\n _=r\n}", false},
		{"proved undeclared symbol", "package p\nfunc f(){\n _=1\n _=0\n _=0\n _=0\n _=0\n _=0\n _=0\n _=r\n}", true},
	}
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			fs := token.NewFileSet()
			f, err := parser.ParseFile(fs, "scope.go", scope.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&types.Config{}).Check("p", fs, []*ast.File{f}, nil)
			if (err != nil) != scope.fails {
				t.Fatalf("scope check: %v", err)
			}
			if scope.fails && !strings.Contains(err.Error(), "undefined: r") {
				t.Fatal(err)
			}
		})
	}
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundle.Core.Content), "Omission from a diff hunk cannot prove absence") {
		t.Fatal("missing explicit omitted-context evidence rule")
	}
}
