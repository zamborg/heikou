package architecture

// The developer-facing docs tell a contributor — increasingly, an agent — what
// to run before pushing. A command that no longer exists is worse than no
// documentation: it is followed, it fails, and the reader has no way to know
// whether the doc or their checkout is wrong.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// makeInvocation matches a backticked `make target`, ignoring any arguments
// after the target name so that `make install PREFIX=/somewhere` still checks
// the install target.
var makeInvocation = regexp.MustCompile("`make ([a-z][a-z-]*)")

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

func TestDocumentedMakeTargetsExist(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	// .PHONY is the Makefile's own list of its targets, so reading it keeps this
	// test from having to guess at the file's structure.
	var declared []string
	for _, line := range strings.Split(makefile, "\n") {
		if rest, found := strings.CutPrefix(line, ".PHONY:"); found {
			declared = append(declared, strings.Fields(rest)...)
		}
	}
	if len(declared) == 0 {
		t.Fatal("the Makefile declares no .PHONY targets; this test can no longer find them")
	}

	for _, doc := range []string{"README.md", filepath.Join("docs", "DEVELOPMENT.md")} {
		for _, match := range makeInvocation.FindAllStringSubmatch(readRepoFile(t, doc), -1) {
			if !slices.Contains(declared, match[1]) {
				t.Errorf("%s tells the reader to run `make %s`, which the Makefile does not define", doc, match[1])
			}
		}
	}
}

func TestDevelopmentDocDescribesTheWorkflowsThatExist(t *testing.T) {
	// The doc's central claim is that merging a version bump is what ships, and
	// that nobody tags by hand. That is only true while the workflow enforcing
	// it is present and still watching main.
	tagWorkflow := readRepoFile(t, filepath.Join(".github", "workflows", "tag.yml"))
	for _, want := range []string{"workflow_run", "branches: [main]", "cmd/h/main.go"} {
		if !strings.Contains(tagWorkflow, want) {
			t.Errorf("tag.yml no longer contains %q; docs/DEVELOPMENT.md promises a workflow that tags main on a version bump", want)
		}
	}

	development := readRepoFile(t, filepath.Join("docs", "DEVELOPMENT.md"))
	// The version lives in exactly one place, and the doc has to name it —
	// pointing a reader at the wrong file is the whole failure this guards.
	if !strings.Contains(development, "cmd/h/main.go") {
		t.Error("docs/DEVELOPMENT.md does not name cmd/h/main.go as where the version lives")
	}
	if !strings.Contains(development, "@latest") {
		t.Error("docs/DEVELOPMENT.md does not explain that @latest resolves to a tag rather than to main")
	}
}
