package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/config"
)

// strokeIdentifiers names the local variables a function dispatches keystrokes
// through. There are two ways a handler gets one: a parameter named for the
// stroke, or a call to a key message's String or Keystroke method. A handler
// written some third way is invisible to this scan, which is why boundChords is
// a declaration rather than something derived from the source alone — the
// declaration is what the reservation test reads.
func strokeIdentifiers(function *ast.FuncDecl) map[string]bool {
	strokes := map[string]bool{}
	if function.Type.Params != nil {
		for _, parameter := range function.Type.Params.List {
			kind, ok := parameter.Type.(*ast.Ident)
			if !ok || kind.Name != "string" {
				continue
			}
			for _, name := range parameter.Names {
				if name.Name == "stroke" || name.Name == "bindingStroke" {
					strokes[name.Name] = true
				}
			}
		}
	}
	ast.Inspect(function, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, value := range assignment.Rhs {
			call, ok := value.(*ast.CallExpr)
			if !ok || index >= len(assignment.Lhs) {
				continue
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (method.Sel.Name != "String" && method.Sel.Name != "Keystroke") {
				continue
			}
			receiver, ok := method.X.(*ast.Ident)
			if !ok || receiver.Name != "key" {
				continue
			}
			if name, ok := assignment.Lhs[index].(*ast.Ident); ok {
				strokes[name.Name] = true
			}
		}
		return true
	})
	return strokes
}

// chordsBoundInSource reads this package's own source and returns every key
// name a handler compares a keystroke against, with where it does so.
func chordsBoundInSource(t *testing.T) map[string]token.Position {
	t.Helper()
	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, parsed)
	}
	if len(files) == 0 {
		t.Fatal("parsed no source files; the scan is looking in the wrong directory")
	}

	// A chord may be written as a named constant — archiveChord is — so the
	// scan resolves package-level string constants before reading the switches.
	constants := map[string]string{}
	for _, file := range files {
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, specification := range group.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					text, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					constants[name.Name] = text
				}
			}
		}
	}

	found := map[string]token.Position{}
	record := func(expression ast.Expr) {
		chord := ""
		switch typed := expression.(type) {
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				return
			}
			text, err := strconv.Unquote(typed.Value)
			if err != nil {
				return
			}
			chord = text
		case *ast.Ident:
			chord = constants[typed.Name]
		}
		if chord == "" {
			return
		}
		if _, seen := found[chord]; !seen {
			found[chord] = fileSet.Position(expression.Pos())
		}
	}

	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			strokes := strokeIdentifiers(function)
			if len(strokes) == 0 {
				continue
			}
			isStroke := func(expression ast.Expr) bool {
				name, ok := expression.(*ast.Ident)
				return ok && strokes[name.Name]
			}
			ast.Inspect(function, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.SwitchStmt:
					if !isStroke(typed.Tag) {
						return true
					}
					for _, statement := range typed.Body.List {
						clause, ok := statement.(*ast.CaseClause)
						if !ok {
							continue
						}
						for _, expression := range clause.List {
							record(expression)
						}
					}
				case *ast.BinaryExpr:
					if typed.Op != token.EQL && typed.Op != token.NEQ {
						return true
					}
					if isStroke(typed.X) {
						record(typed.Y)
					} else if isStroke(typed.Y) {
						record(typed.X)
					}
				}
				return true
			})
		}
	}
	return found
}

// The list and the switches have to describe the same keyboard. Checking both
// directions is what makes the list trustworthy: a chord added to a switch is
// caught as undeclared, and a chord deleted from a switch is caught as a stale
// declaration that would keep a key reserved for nothing.
func TestEveryChordTheUIBindsIsDeclaredHere(t *testing.T) {
	found := chordsBoundInSource(t)
	if len(found) == 0 {
		t.Fatal("found no keystroke comparisons; the scan no longer recognizes this package's key handlers")
	}
	for chord, position := range found {
		if !slices.Contains(boundChords, chord) {
			t.Errorf("%s dispatches on %q, which boundChords does not declare; declare it there so internal/config reserves it", position, chord)
		}
	}
	for _, chord := range boundChords {
		if _, bound := found[chord]; !bound {
			t.Errorf("boundChords declares %q, but no handler in this package compares a keystroke against it", chord)
		}
	}
	for index, chord := range boundChords {
		if slices.Index(boundChords, chord) != index {
			t.Errorf("boundChords lists %q twice", chord)
		}
	}
}

// A key the dashboard already answers to must not also be assignable to a
// composer binding: the binding is consulted first, so the dashboard action
// simply stops happening, with nothing said about it at any point. This is the
// assertion the two lists exist for, and it lives here because ui may import
// config while config may not import ui.
func TestEveryBoundChordIsReservedFromComposerBindings(t *testing.T) {
	reserved := config.ReservedComposerKeys()
	for _, chord := range boundChords {
		use, found := reserved[chord]
		if !found {
			t.Errorf("the dashboard binds %q, but settings would accept it as a composer binding; reserve it in internal/config", chord)
			continue
		}
		if strings.TrimSpace(use) == "" {
			t.Errorf("internal/config reserves %q without saying what it does; a rejected settings file quotes that text", chord)
		}
	}
	for chord := range reserved {
		if !slices.Contains(boundChords, chord) {
			t.Errorf("internal/config reserves %q, which nothing here binds; a key held back for nothing is one a user cannot have for no reason", chord)
		}
	}
}
