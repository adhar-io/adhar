package get

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every section heading in `adhar get status` must sit behind the CLI's one
// gutter. The "Issues & Warnings" heading was rendered with helpers.WarningStyle,
// which carries no left margin, so on a cluster with warnings that one heading
// hung two columns left of "Core Services", "Cluster Resources" and every
// bordered box beneath it.
//
// The fix is not to give WarningStyle a margin: it is an INLINE styler used in
// thirty-odd table cells and sentences, and a margin there would indent all of
// them. Headings take TitleStyle, recoloured where needed.
//
// Matched on the AST rather than on the file text, so a heading named in a
// comment cannot satisfy or break this guard.
func TestEveryStatusSectionHeadingSitsBehindTheGutter(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "status.go", nil, 0)
	if err != nil {
		t.Fatalf("parse status.go: %v", err)
	}

	// The heading idiom: fmt.Printf("\n%s\n", <style>.Render(<text>)).
	const headingFormat = `"\n%s\n"`

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 || !isSelector(call.Fun, "fmt", "Printf") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || lit.Value != headingFormat {
			return true
		}
		render, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := render.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Render" {
			return true
		}
		found++
		root := styleRoot(sel.X)
		if root != "helpers.TitleStyle" {
			t.Errorf("heading at %s renders with %s; section headings must build on helpers.TitleStyle, which carries the CLI gutter (recolour it with .Foreground(...) if the heading is a warning)",
				fset.Position(call.Pos()), root)
		}
		return true
	})

	if found < 3 {
		t.Fatalf("found only %d section headings; the idiom this guard matches has changed and it is no longer checking anything", found)
	}
}

// styleRoot walks back down a chained style expression such as
// helpers.TitleStyle.Foreground(x).Bold(true) to its base, "helpers.TitleStyle".
func styleRoot(e ast.Expr) string {
	for {
		switch v := e.(type) {
		case *ast.CallExpr:
			e = v.Fun
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok {
				return pkg.Name + "." + v.Sel.Name
			}
			e = v.X
		case *ast.Ident:
			return v.Name
		default:
			return "<unrecognised style expression>"
		}
	}
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}
