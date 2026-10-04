package up

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Nothing may print to the terminal while a StageTracker is running.
//
// The tracker owns the cursor between startCloudProgress and restoreProgress:
// each tick it repositions by moving up `lastLines` and clearing to the end of
// the screen. A plain fmt.Print underneath it scrolls the block without the
// tracker knowing, so the next move-up lands in the wrong place and leaves an
// orphaned, truncated copy of the checklist on screen:
//
//	Provisioning prod  22m53s     <- orphan, cut off where it scrolled
//	✓  Preflight  0s
//	✓  Cloud cluster  3m56s
//	Provisioning prod  22m53s     <- the live block
//	✓  Preflight  0s
//	...
//
// Both copies carried the SAME elapsed time, which is the tell: they were
// milliseconds apart, not one stale and one fresh.
//
// Two things make this harder to check than it looks, and getting either wrong
// produces a test that passes against the bug:
//
//   - the culprit was not an inline print. createEnvironmentNamespaces prints a
//     line per namespace and was called from inside the provisioning loop, so a
//     scan for fmt.Print in that loop finds nothing. The check has to follow
//     calls into the functions they reach.
//   - source order is not execution order. The loop's failure paths call
//     restoreProgress(true) and then print, several lines ABOVE the success
//     path — but they end in `continue`, so they say nothing about whether the
//     tracker is still live further down. A linear sweep concludes the tracker
//     was already stopped and clears the very finding it exists to make.
//
// Hence a small flow-sensitive walk: branches that bail out with continue,
// break or return do not propagate their state to what follows. Anything that
// must print while the tracker is live goes through tracker.Log, which clears
// the block first and redraws it after.
func TestNothingPrintsWhileTheStageTrackerIsLive(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && fn.Recv == nil {
					funcs[fn.Name.Name] = fn
				}
			}
		}
	}
	if len(funcs) == 0 {
		t.Fatal("no functions parsed — the guard is not looking at anything")
	}

	prints, direct := printingFuncs(funcs)
	c := &checker{t: t, fset: fset, prints: prints, seen: map[string]bool{}}

	// Self-check: the transitive step must actually do something.
	//
	// The culprit this test was written for printed from a CALLEE, so if the
	// closure silently stopped working the whole test would pass while detecting
	// nothing. Asserted on the mechanism rather than on a named function: the
	// first version named createEnvironmentNamespaces, and when that function
	// legitimately stopped printing the canary failed for no real reason.
	if len(prints) <= len(direct) {
		t.Fatalf("the call-graph closure found nothing beyond the %d direct printers — a function "+
			"that prints only via a callee would go undetected, which is exactly the case this "+
			"test exists for", len(direct))
	}

	started := false
	for _, fn := range funcs {
		if !bodyMentions(fn, "startCloudProgress") {
			continue
		}
		started = true
		c.walk(fn.Body.List, false)
	}
	if !started {
		t.Fatal("no function calls startCloudProgress — nothing was checked")
	}
}

type checker struct {
	t      *testing.T
	fset   *token.FileSet
	prints map[string]bool
	// Loop bodies are walked twice to settle the back-edge, so a finding inside
	// one would otherwise be reported twice.
	seen map[string]bool
}

// walk returns whether a tracker is live after these statements.
func (c *checker) walk(stmts []ast.Stmt, live bool) bool {
	for _, st := range stmts {
		live = c.stmt(st, live)
	}
	return live
}

func (c *checker) stmt(st ast.Stmt, live bool) bool {
	switch s := st.(type) {
	case *ast.IfStmt:
		live = c.calls(s.Init, live)
		live = c.calls(s.Cond, live)
		live = c.branch(s.Body, live)
		if s.Else != nil {
			live = c.branch(s.Else, live)
		}
		return live
	case *ast.BlockStmt:
		return c.walk(s.List, live)
	case *ast.ForStmt:
		live = c.calls(s.Init, live)
		// Twice, so a second iteration sees the state the first one left.
		live = c.walk(s.Body.List, live)
		return c.walk(s.Body.List, live)
	case *ast.RangeStmt:
		live = c.walk(s.Body.List, live)
		return c.walk(s.Body.List, live)
	case *ast.SwitchStmt:
		live = c.calls(s.Init, live)
		live = c.calls(s.Tag, live)
		for _, cl := range s.Body.List {
			live = c.branch(cl, live)
		}
		return live
	case *ast.TypeSwitchStmt:
		for _, cl := range s.Body.List {
			live = c.branch(cl, live)
		}
		return live
	case *ast.SelectStmt:
		for _, cl := range s.Body.List {
			live = c.branch(cl, live)
		}
		return live
	case *ast.LabeledStmt:
		return c.stmt(s.Stmt, live)
	default:
		return c.calls(st, live)
	}
}

// branch analyses a nested block. A block that bails out with continue, break
// or return tells us nothing about the code that follows it, so its state is
// discarded; otherwise a path that leaves the tracker live makes it live.
func (c *checker) branch(n ast.Node, live bool) bool {
	var body []ast.Stmt
	switch b := n.(type) {
	case *ast.BlockStmt:
		body = b.List
	case *ast.CaseClause:
		body = b.Body
	case *ast.CommClause:
		body = b.Body
	case nil:
		return live
	default:
		return c.calls(n, live)
	}
	after := c.walk(body, live)
	if terminates(body) {
		return live
	}
	return live || after
}

func terminates(body []ast.Stmt) bool {
	if len(body) == 0 {
		return false
	}
	switch last := body[len(body)-1].(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return last.Tok == token.CONTINUE || last.Tok == token.BREAK || last.Tok == token.GOTO
	}
	return false
}

// calls inspects a leaf node's calls in source order, reporting any print that
// happens while a tracker is live.
func (c *checker) calls(n ast.Node, live bool) bool {
	if n == nil {
		return live
	}
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, printer := callTarget(call, c.prints)
		switch {
		case name == "startCloudProgress":
			live = true
		case name == "restoreProgress":
			live = false
		case printer && live:
			pos := c.fset.Position(call.Pos())
			if c.seen[pos.String()] {
				return true
			}
			c.seen[pos.String()] = true
			c.t.Errorf("%s:%d: %s prints while a StageTracker is live — that scrolls the checklist "+
				"out from under the tracker's cursor arithmetic and leaves an orphaned copy on "+
				"screen. Move it after restoreProgress, or route it through tracker.Log.",
				pos.Filename, pos.Line, name)
		}
		return true
	})
	return live
}

// callTarget names the callee and says whether calling it prints.
func callTarget(call *ast.CallExpr, prints map[string]bool) (string, bool) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, prints[fun.Name]
	case *ast.SelectorExpr:
		if isStdoutPrint(fun) {
			return "fmt." + fun.Sel.Name, true
		}
	}
	return "", false
}

// printingFuncs returns the set of package functions that write to the terminal
// directly or through a callee, plus the subset that print directly. The caller
// compares the two to prove the transitive step is still working.
func printingFuncs(funcs map[string]*ast.FuncDecl) (all, direct map[string]bool) {
	prints := map[string]bool{}
	callees := map[string]map[string]bool{}
	for name, fn := range funcs {
		callees[name] = map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				callees[name][fun.Name] = true
			case *ast.SelectorExpr:
				if isStdoutPrint(fun) {
					prints[name] = true
				}
			}
			return true
		})
	}
	direct = map[string]bool{}
	for fn := range prints {
		direct[fn] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, cs := range callees {
			if prints[fn] {
				continue
			}
			for callee := range cs {
				if prints[callee] {
					prints[fn], changed = true, true
					break
				}
			}
		}
	}
	return prints, direct
}

func bodyMentions(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// isStdoutPrint reports whether a selector call writes to the terminal.
// Sprintf and friends format without printing, so they are excluded.
func isStdoutPrint(sel *ast.SelectorExpr) bool {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "fmt" {
		return false
	}
	switch sel.Sel.Name {
	case "Print", "Printf", "Println":
		return true
	}
	return false
}
