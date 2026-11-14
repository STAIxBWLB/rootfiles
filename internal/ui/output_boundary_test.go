package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The output boundary is a source contract: machine payloads may use explicit
// fmt.Fprint writers, but native command/module text must never bypass one.
func TestOutputBoundaryHasNoBarePrintsOrExternalGlyphLiterals(t *testing.T) {
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "ui" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.ContainsAny(string(data), MarkOK+MarkFail+MarkWarn+MarkNext+MarkSection) {
			t.Errorf("%s has a glyph literal outside ui; use an unstyled ui marker constant", p)
		}
		parent := filepath.Base(filepath.Dir(p))
		if parent != "module" && parent != "cli" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), p, data, 0)
		if err != nil {
			return err
		}
		fmtName := ""
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if path == "fmt" {
				fmtName = "fmt"
				if imp.Name != nil {
					fmtName = imp.Name.Name
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				value, err := strconv.Unquote(lit.Value)
				if err == nil && strings.ContainsRune(value, 27) {
					t.Errorf("%s contains a raw terminal escape outside ui", p)
				}
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name, ok := sel.X.(*ast.Ident)
			if ok && fmtName != "" && name.Name == fmtName && strings.HasPrefix(sel.Sel.Name, "Print") {
				t.Errorf("%s uses stdout-only fmt.%s; pass a context/command writer through a ui helper", p, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
