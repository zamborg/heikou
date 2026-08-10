package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/workstream"
)

func TestArtifactContextBoundsNotesAndTree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte(strings.Repeat("é", artifactContextMaxNotesBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < artifactContextMaxEntries+10; index++ {
		name := filepath.Join(root, formatArtifactTestName("file", index))
		if err := os.WriteFile(name, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(root), defaultArtifactContextLimits())
	if snapshot.NotesStatus != artifactNotesReady || !snapshot.NotesTruncated {
		t.Fatalf("notes status = %q, truncated = %v", snapshot.NotesStatus, snapshot.NotesTruncated)
	}
	if len(snapshot.Notes) > artifactContextMaxNotesBytes || !utf8.ValidString(snapshot.Notes) {
		t.Fatalf("notes length/UTF-8 = %d/%v", len(snapshot.Notes), utf8.ValidString(snapshot.Notes))
	}
	if len(snapshot.Tree) != artifactContextMaxEntries || !snapshot.TreeTruncated {
		t.Fatalf("tree entries/truncated = %d/%v", len(snapshot.Tree), snapshot.TreeTruncated)
	}
	for _, entry := range snapshot.Tree {
		if entry.RelativePath == "notes.md" {
			t.Fatal("root notes.md was duplicated in the tree")
		}
	}
}

func TestArtifactContextMissingStatesAreReadOnly(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-created")
	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(missing), artifactContextLimits{})
	if snapshot.NotesStatus != artifactNotesUnavailable || snapshot.NotesError == "" || snapshot.TreeError == "" {
		t.Fatalf("missing directory snapshot = %#v", snapshot)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("reader created missing artifact directory: %v", err)
	}

	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot = readArtifactContext(context.Background(), artifactTestWorkstream(empty), artifactContextLimits{})
	if snapshot.NotesStatus != artifactNotesMissing || snapshot.NotesError != "" {
		t.Fatalf("missing notes state = %q, error %q", snapshot.NotesStatus, snapshot.NotesError)
	}
	if len(snapshot.Tree) != 0 || snapshot.TreeError != "" || snapshot.TreeTruncated {
		t.Fatalf("empty tree snapshot = %#v", snapshot)
	}
}

func TestArtifactContextTreeIsStableShallowAndNotesAreNotDuplicated(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"z-dir", "a-dir", filepath.Join("a-dir", "child"), filepath.Join("a-dir", "child", "too-deep")} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"z-file", "a-file", "notes.md", filepath.Join("a-dir", "notes.md"), filepath.Join("a-dir", "child", "hidden.txt")} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(root), defaultArtifactContextLimits())
	var rootEntries []string
	for _, entry := range snapshot.Tree {
		if entry.Depth == 1 {
			rootEntries = append(rootEntries, entry.Name)
		}
		if entry.Depth > artifactContextMaxDepth {
			t.Fatalf("entry escaped depth bound: %#v", entry)
		}
		if entry.RelativePath == "notes.md" || entry.RelativePath == "a-dir/child/hidden.txt" {
			t.Fatalf("unexpected tree entry: %#v", entry)
		}
	}
	if want := []string{"a-dir", "z-dir", "a-file", "z-file"}; !slices.Equal(rootEntries, want) {
		t.Fatalf("root order = %#v, want %#v", rootEntries, want)
	}
	if !artifactTreeContains(snapshot.Tree, "a-dir/notes.md") {
		t.Fatal("nested notes.md should remain visible in the tree")
	}
	if !snapshot.TreeTruncated {
		t.Fatal("depth-limited directory did not mark the tree truncated")
	}
}

func TestArtifactContextHonorsOpenedDirectoryLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 5; index++ {
		directory := filepath.Join(root, formatArtifactTestName("dir", index))
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "child.txt"), []byte("child"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	limits := defaultArtifactContextLimits()
	limits.Directories = 2 // Root plus exactly one child directory.
	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(root), limits)
	children := 0
	for _, entry := range snapshot.Tree {
		if entry.Depth == 2 {
			children++
		}
	}
	if children != 1 || !snapshot.TreeTruncated {
		t.Fatalf("depth-two children/truncated = %d/%v", children, snapshot.TreeTruncated)
	}
}

func TestArtifactContextRejectsSymlinks(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("must not be read"), 0o600); err != nil {
		t.Fatal(err)
	}

	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(rootLink), artifactContextLimits{})
	if snapshot.TreeError == "" || !strings.Contains(snapshot.TreeError, "symlink") || len(snapshot.Tree) != 0 {
		t.Fatalf("symlink root snapshot = %#v", snapshot)
	}

	if err := os.Symlink(secret, filepath.Join(realRoot, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(realRoot, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	snapshot = readArtifactContext(context.Background(), artifactTestWorkstream(realRoot), artifactContextLimits{})
	if snapshot.NotesStatus != artifactNotesUnavailable || !strings.Contains(snapshot.NotesError, "symlink") || strings.Contains(snapshot.Notes, "must not") {
		t.Fatalf("symlink notes snapshot = %#v", snapshot)
	}
	var linked *artifactTreeEntry
	for index := range snapshot.Tree {
		if snapshot.Tree[index].Name == "linked-dir" {
			linked = &snapshot.Tree[index]
		}
		if strings.Contains(snapshot.Tree[index].RelativePath, "secret.txt") {
			t.Fatal("tree descended through a symlink")
		}
	}
	if linked == nil || !linked.Symlink || linked.Directory {
		t.Fatalf("linked directory entry = %#v", linked)
	}
}

func TestArtifactContextMakesInvalidUTF8NotesValid(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte{'a', 0xff, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(root), artifactContextLimits{})
	if snapshot.NotesStatus != artifactNotesReady || !utf8.ValidString(snapshot.Notes) {
		t.Fatalf("invalid UTF-8 snapshot = %#v", snapshot)
	}
	if !strings.Contains(snapshot.Notes, "�") {
		t.Fatalf("invalid byte replacement missing from %q", snapshot.Notes)
	}
}

func TestArtifactContextRejectsNonRegularNotesAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "notes.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := readArtifactContext(context.Background(), artifactTestWorkstream(root), artifactContextLimits{})
	if snapshot.NotesStatus != artifactNotesUnavailable || !strings.Contains(snapshot.NotesError, "regular file") {
		t.Fatalf("notes directory snapshot = %#v", snapshot)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot = readArtifactContext(ctx, artifactTestWorkstream(root), artifactContextLimits{})
	if snapshot.NotesError != context.Canceled.Error() || snapshot.TreeError != context.Canceled.Error() || len(snapshot.Tree) != 0 {
		t.Fatalf("canceled snapshot = %#v", snapshot)
	}
}

func artifactTestWorkstream(root string) workstream.Workstream {
	return workstream.Workstream{ID: "artifact-test", ArtifactDir: root}
}

func artifactTreeContains(entries []artifactTreeEntry, relativePath string) bool {
	for _, entry := range entries {
		if entry.RelativePath == relativePath {
			return true
		}
	}
	return false
}

func formatArtifactTestName(prefix string, index int) string {
	const digits = "000"
	value := []byte(digits)
	for position := len(value) - 1; position >= 0; position-- {
		value[position] = byte('0' + index%10)
		index /= 10
	}
	return prefix + "-" + string(value)
}
