package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestAuthEnforcement_GateAware is the SOUNDNESS half of the §16 audit (fix-46
// finding 46.C, team-lead "be gate-aware, not marker-aware"). The marker test
// (TestAuthEnforcement_GatedSet) only checks the gatedAnnotation — it cannot catch
// a command that calls gateLocation but is NOT annotated (a real gate the audit is
// blind to — exactly how `gc`/`rebuild-catalog` slipped) nor one annotated but not
// gated (a lie). This test binds the two by STATIC CALL ANALYSIS of the package:
// it asserts, for every command constructor, that the command's RunE actually
// reaches gateLocation IF AND ONLY IF the constructor calls markGated. So gate and
// annotation can never drift apart again.
//
// (The behavioral proof — driving each gated command with a wrong password and
// asserting TV-AUTH-01 + zero mutation — lives in the task-50 auth matrix against
// the fedtest auth-verifier seam; that is what proves the gate REJECTS. This test
// proves every actual gate IS in the annotated set the marker audit reasons about.)
func TestAuthEnforcement_GateAware(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg := pkgs["cli"]
	if pkg == nil {
		t.Fatal("package cli not found in .")
	}

	ctorRe := regexp.MustCompile(`^new.*Cmd$`)
	isCtor := func(name string) bool { return ctorRe.MatchString(name) }

	// Collect every top-level (non-method) func decl by name.
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name != nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}

	// refs(fn) = the set of identifier names referenced in fn's body, EXCLUDING the
	// selector field of x.Y (so only package-local idents like gateLocation /
	// runVaultMv / markGated count, not foreign methods).
	refs := func(fd *ast.FuncDecl) map[string]bool {
		skip := map[*ast.Ident]bool{}
		ast.Inspect(fd, func(n ast.Node) bool {
			if se, ok := n.(*ast.SelectorExpr); ok {
				skip[se.Sel] = true
			}
			return true
		})
		out := map[string]bool{}
		ast.Inspect(fd, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && !skip[id] {
				out[id.Name] = true
			}
			return true
		})
		return out
	}
	refsOf := map[string]map[string]bool{}
	for name, fd := range funcs {
		refsOf[name] = refs(fd)
	}

	// gatingSet = the transitive closure of "reaches gateLocation", computed over
	// NON-constructor functions only (helpers + run funcs). Constructors are
	// EXCLUDED from the set so a parent group (newVaultCmd → newVaultMvCmd) does not
	// inherit "gating" from a gated child — only RunE dispatch into a gating run
	// func counts.
	gating := map[string]bool{"gateLocation": true}
	for changed := true; changed; {
		changed = false
		for name, rs := range refsOf {
			if isCtor(name) || gating[name] {
				continue
			}
			for g := range gating {
				if rs[g] {
					gating[name] = true
					changed = true
					break
				}
			}
		}
	}

	// For every command constructor: gates := its body reaches a gating func;
	// marked := it calls markGated. Assert gates ⟺ marked.
	for name := range funcs {
		if !isCtor(name) {
			continue
		}
		rs := refsOf[name]
		gates := false
		for g := range gating {
			if rs[g] {
				gates = true
				break
			}
		}
		marked := rs["markGated"]
		switch {
		case gates && !marked:
			t.Errorf("%s reaches gateLocation but is NOT markGated — a real gate invisible to the §16 marker audit (annotate it, or it can be silently un-gated)", name)
		case marked && !gates:
			t.Errorf("%s is markGated but its RunE never reaches gateLocation — an annotation lie (the command claims to gate but does not)", name)
		}
	}
}
