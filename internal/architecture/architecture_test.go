package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/zamborg/heikou"

// allowedImports is the intended shape of the module. Each key is a package
// directory relative to the module root; each value is every package inside the
// module that it may import, test files included.
//
// The layering it describes:
//
//   - env, format and heikou are leaves. They import nothing from the module,
//     so any layer can use them and none of them can create a cycle.
//   - home, config, runner and workstream own one resource each — the install
//     directory, settings, runner argv, durable state — and know nothing about
//     tmux, the controller, or the terminal.
//   - supervisor owns tmux. control owns lifecycle policy and is the only
//     package allowed to combine a supervisor with a store.
//   - ui renders and never reaches past control.Service.
//   - cmd/h wires the process together and is the only package permitted to
//     depend on nearly everything.
//
// A package missing from this map fails rather than defaulting to permitted, so
// a new package has to be placed in the design deliberately.
var allowedImports = map[string][]string{
	"cmd/h": {
		"internal/config", "internal/control", "internal/control/controltest",
		"internal/env", "internal/format", "internal/heikou", "internal/home",
		"internal/runner", "internal/supervisor", "internal/ui", "internal/workstream",
		"skills/learn-heikou", "skills/manage-heikou",
	},
	"internal/architecture":        {},
	"internal/config":              {"internal/env", "internal/heikou", "internal/home"},
	"internal/control":             {"internal/heikou", "internal/workstream"},
	"internal/control/controltest": {"internal/control", "internal/workstream"},
	"internal/env":                 {},
	"internal/format":              {},
	"internal/heikou":              {},
	"internal/home":                {"internal/env"},
	"internal/runner":              {"internal/env", "internal/heikou"},
	"internal/supervisor":          {"internal/env", "internal/format", "internal/heikou", "internal/runner"},
	"internal/ui": {
		"internal/config", "internal/control", "internal/control/controltest",
		"internal/format", "internal/heikou", "internal/runner", "internal/workstream",
	},
	"internal/workstream":  {"internal/env", "internal/heikou", "internal/home"},
	"skills/learn-heikou":  {},
	"skills/manage-heikou": {},
}

// singleHomeFunctions names the shared helpers that were forked across packages
// and drifted, and the package that now owns each. The test catches a copy that
// keeps the original name, which is how all four forks actually happened. It
// cannot catch a copy under a new name; nothing cheap can.
var singleHomeFunctions = map[string]string{
	"formatDuration": "internal/format",
	"relativeTime":   "internal/format",
	"shortID":        "internal/format",
	"compactPath":    "internal/format",
	"oneLine":        "internal/format",
	"sanitize":       "internal/format",
}

// goFile is one parsed source file with the package directory it belongs to.
type goFile struct {
	pkg  string // directory relative to the module root
	path string
	test bool
	ast  *ast.File
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}

// parseModule reads every Go file in the module. It is deliberately a plain
// filesystem walk rather than go/packages: the point is to see the source as
// written, including files a build constraint would hide.
func parseModule(t *testing.T) []goFile {
	t.Helper()
	root := moduleRoot(t)
	fileSet := token.NewFileSet()
	var files []goFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".github", ".claude", "bin", "testdata", "docs", "todos":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		files = append(files, goFile{
			pkg:  filepath.ToSlash(relative),
			path: filepath.ToSlash(mustRel(t, root, path)),
			test: strings.HasSuffix(path, "_test.go"),
			ast:  parsed,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("parsed no Go files; the walk is looking in the wrong place")
	}
	return files
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	relative, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatalf("relative path for %q: %v", path, err)
	}
	return relative
}

func TestPackagesImportOnlyTheirDeclaredLayer(t *testing.T) {
	observed := map[string]map[string]bool{}
	for _, file := range parseModule(t) {
		allowed, declared := allowedImports[file.pkg]
		if !declared {
			t.Errorf("package %s is not placed in allowedImports; add it to the layering deliberately", file.pkg)
			continue
		}
		for _, spec := range file.ast.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", file.path, spec.Path.Value, err)
			}
			inner, found := strings.CutPrefix(path, modulePath+"/")
			if !found {
				continue
			}
			if observed[file.pkg] == nil {
				observed[file.pkg] = map[string]bool{}
			}
			observed[file.pkg][inner] = true
			if !slices.Contains(allowed, inner) {
				t.Errorf("%s imports %s, which %s is not allowed to depend on", file.path, inner, file.pkg)
			}
		}
	}

	// A permission nobody uses is stale rather than dangerous, so it is reported
	// without failing: tightening the map should be a deliberate edit, not the
	// price of deleting an import.
	for pkg, allowed := range allowedImports {
		for _, dependency := range allowed {
			if !observed[pkg][dependency] {
				t.Logf("allowedImports lets %s import %s, but it no longer does", pkg, dependency)
			}
		}
	}
}

func TestTheDependencyGraphHasNoCycles(t *testing.T) {
	// The compiler already rejects an import cycle, so this does not guard the
	// build. It guards the map above: a cycle written into allowedImports would
	// mean the intended design is one the compiler could never accept, and the
	// map is what a reader trusts.
	// A package absent from state is unvisited, which is the zero value.
	const (
		onStack = iota + 1
		done
	)
	state := map[string]int{}
	var path []string
	// One cycle is reported and the walk stops. Continuing would re-report the
	// same edge from every entry point that reaches it, burying the one line a
	// reader needs under a dozen near-identical paths.
	var cycle string
	var visit func(string)
	visit = func(pkg string) {
		if cycle != "" || state[pkg] == done {
			return
		}
		if state[pkg] == onStack {
			cycle = strings.Join(path, " -> ") + " -> " + pkg
			return
		}
		state[pkg] = onStack
		path = append(path, pkg)
		for _, dependency := range allowedImports[pkg] {
			visit(dependency)
		}
		path = path[:len(path)-1]
		state[pkg] = done
	}
	for _, pkg := range slices.Sorted(maps.Keys(allowedImports)) {
		visit(pkg)
	}
	if cycle != "" {
		t.Errorf("allowedImports describes a cycle: %s", cycle)
	}
}

func TestSharedHelpersKeepASingleHome(t *testing.T) {
	for _, file := range parseModule(t) {
		for _, declaration := range file.ast.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil {
				continue
			}
			owner, shared := singleHomeFunctions[function.Name.Name]
			if !shared || file.pkg == owner {
				continue
			}
			t.Errorf("%s declares %s, which belongs to %s; these helpers were forked once already and drifted",
				file.path, function.Name.Name, owner)
		}
	}
}

func TestEnvironmentVariableNamesAreWrittenInOnePlace(t *testing.T) {
	for _, file := range parseModule(t) {
		// Test files legitimately build environments for subprocesses, and the
		// end-to-end harness blanks names it does not otherwise reference.
		if file.test || file.pkg == "internal/env" {
			continue
		}
		ast.Inspect(file.ast, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || !strings.HasPrefix(value, "HEIKOU_") {
				return true
			}
			t.Errorf("%s writes %q as a literal; name it in internal/env so one list answers which variables Heikou honours",
				file.path, value)
			return true
		})
	}
}

func TestEveryDeclaredNameIsReachableFromTheBinary(t *testing.T) {
	// cmd/h is the only entry point. A package no path from it can reach is
	// dead weight that still has to be maintained, so it should be noticed.
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(pkg string) {
		if reachable[pkg] {
			return
		}
		reachable[pkg] = true
		for _, dependency := range allowedImports[pkg] {
			visit(dependency)
		}
	}
	visit("cmd/h")
	for pkg := range allowedImports {
		if pkg == "internal/architecture" {
			continue // holds tests only, and is reached by no one on purpose
		}
		if !reachable[pkg] {
			t.Errorf("no import path reaches %s from cmd/h", pkg)
		}
	}
}
