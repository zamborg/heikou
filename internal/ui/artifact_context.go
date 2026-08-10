package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/workstream"
)

const (
	artifactContextMaxNotesBytes  = 16 * 1024
	artifactContextMaxEntries     = 64
	artifactContextMaxDirectories = 12
	artifactContextMaxDepth       = 2
)

type artifactContextLimits struct {
	NotesBytes  int
	Entries     int
	Directories int
	Depth       int
}

func defaultArtifactContextLimits() artifactContextLimits {
	return artifactContextLimits{
		NotesBytes:  artifactContextMaxNotesBytes,
		Entries:     artifactContextMaxEntries,
		Directories: artifactContextMaxDirectories,
		Depth:       artifactContextMaxDepth,
	}
}

func (limits artifactContextLimits) bounded() artifactContextLimits {
	defaults := defaultArtifactContextLimits()
	limits.NotesBytes = boundedArtifactLimit(limits.NotesBytes, defaults.NotesBytes)
	limits.Entries = boundedArtifactLimit(limits.Entries, defaults.Entries)
	limits.Directories = boundedArtifactLimit(limits.Directories, defaults.Directories)
	limits.Depth = boundedArtifactLimit(limits.Depth, defaults.Depth)
	return limits
}

func boundedArtifactLimit(value, maximum int) int {
	if value <= 0 || value > maximum {
		return maximum
	}
	return value
}

type artifactNotesStatus string

const (
	artifactNotesReady       artifactNotesStatus = "ready"
	artifactNotesMissing     artifactNotesStatus = "missing"
	artifactNotesUnavailable artifactNotesStatus = "unavailable"
)

type artifactTreeEntry struct {
	Name         string
	RelativePath string
	Depth        int
	Directory    bool
	Regular      bool
	Symlink      bool
	Error        string
}

type artifactContextSnapshot struct {
	WorkstreamID string
	ArtifactDir  string

	Notes          string
	NotesStatus    artifactNotesStatus
	NotesTruncated bool
	NotesError     string

	Tree          []artifactTreeEntry
	TreeTruncated bool
	TreeError     string
}

func readArtifactContext(ctx context.Context, item workstream.Workstream, limits artifactContextLimits) artifactContextSnapshot {
	snapshot := artifactContextSnapshot{
		WorkstreamID: item.ID,
		ArtifactDir:  item.ArtifactDir,
		NotesStatus:  artifactNotesUnavailable,
	}
	limits = limits.bounded()
	if err := ctx.Err(); err != nil {
		snapshot.NotesError = err.Error()
		snapshot.TreeError = err.Error()
		return snapshot
	}

	rootInfo, err := os.Lstat(item.ArtifactDir)
	if err != nil {
		message := artifactFilesystemError("inspect artifact directory", err)
		if errors.Is(err, os.ErrNotExist) {
			message = "artifact directory is missing"
		}
		snapshot.NotesError = message
		snapshot.TreeError = message
		return snapshot
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		message := "artifact directory is a symlink"
		snapshot.NotesError = message
		snapshot.TreeError = message
		return snapshot
	}
	if !rootInfo.IsDir() {
		message := "artifact path is not a directory"
		snapshot.NotesError = message
		snapshot.TreeError = message
		return snapshot
	}

	readArtifactNotes(ctx, item.ArtifactDir, limits.NotesBytes, &snapshot)
	if err := ctx.Err(); err != nil {
		if snapshot.NotesStatus != artifactNotesReady {
			snapshot.NotesStatus = artifactNotesUnavailable
			snapshot.NotesError = err.Error()
		}
		snapshot.TreeError = err.Error()
		return snapshot
	}

	loader := artifactTreeLoader{ctx: ctx, limits: limits, snapshot: &snapshot}
	if message := loader.walk(item.ArtifactDir, "", 0, true); message != "" {
		loader.recordError(message)
	}
	return snapshot
}

func readArtifactNotes(ctx context.Context, artifactDir string, maximum int, snapshot *artifactContextSnapshot) {
	if err := ctx.Err(); err != nil {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = err.Error()
		return
	}

	path := filepath.Join(artifactDir, "notes.md")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			snapshot.NotesStatus = artifactNotesMissing
			return
		}
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = artifactFilesystemError("inspect notes.md", err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = "notes.md is a symlink"
		return
	}
	if !info.Mode().IsRegular() {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = "notes.md is not a regular file"
		return
	}

	file, err := os.Open(path)
	if err != nil {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = artifactFilesystemError("open notes.md", err)
		return
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = artifactFilesystemError("inspect open notes.md", err)
		return
	}
	if !openedInfo.Mode().IsRegular() {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = "notes.md changed into a non-regular file"
		return
	}
	if !os.SameFile(info, openedInfo) {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = "notes.md changed before it could be read"
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = artifactFilesystemError("read notes.md", err)
		return
	}
	if err := ctx.Err(); err != nil {
		snapshot.NotesStatus = artifactNotesUnavailable
		snapshot.NotesError = err.Error()
		return
	}
	if len(data) > maximum {
		data = data[:maximum]
		snapshot.NotesTruncated = true
	}
	text := strings.ToValidUTF8(string(data), "�")
	if clipped, truncated := clipArtifactUTF8(text, maximum); truncated {
		text = clipped
		snapshot.NotesTruncated = true
	}
	snapshot.Notes = text
	snapshot.NotesStatus = artifactNotesReady
}

func clipArtifactUTF8(value string, maximum int) (string, bool) {
	if len(value) <= maximum {
		return value, false
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

type artifactTreeLoader struct {
	ctx        context.Context
	limits     artifactContextLimits
	snapshot   *artifactContextSnapshot
	openedDirs int
}

func (loader *artifactTreeLoader) walk(path, relative string, parentDepth int, root bool) string {
	if err := loader.ctx.Err(); err != nil {
		loader.snapshot.TreeTruncated = true
		return err.Error()
	}
	if len(loader.snapshot.Tree) >= loader.limits.Entries {
		loader.snapshot.TreeTruncated = true
		return ""
	}
	if loader.openedDirs >= loader.limits.Directories {
		loader.snapshot.TreeTruncated = true
		return ""
	}

	info, err := os.Lstat(path)
	if err != nil {
		return artifactFilesystemError("inspect artifact directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "artifact directory became a symlink"
	}
	if !info.IsDir() {
		return "artifact tree entry is not a directory"
	}

	directory, err := os.Open(path)
	if err != nil {
		return artifactFilesystemError("open artifact directory", err)
	}
	loader.openedDirs++
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil {
		return artifactFilesystemError("inspect open artifact directory", err)
	}
	if !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
		return "artifact directory changed before it could be read"
	}

	remaining := loader.limits.Entries - len(loader.snapshot.Tree)
	readLimit := remaining + 1
	if root {
		// Root notes are rendered separately, so reserve one bounded slot for
		// filtering notes.md without reducing the visible tree budget.
		readLimit++
	}
	entries, readErr := directory.ReadDir(readLimit)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		loader.recordError(artifactFilesystemError("read artifact directory", readErr))
	}

	filtered := entries[:0]
	for _, entry := range entries {
		if root && entry.Name() == "notes.md" {
			continue
		}
		filtered = append(filtered, entry)
	}
	entries = filtered
	sort.SliceStable(entries, func(left, right int) bool {
		leftDirectory := entries[left].IsDir() && entries[left].Type()&os.ModeSymlink == 0
		rightDirectory := entries[right].IsDir() && entries[right].Type()&os.ModeSymlink == 0
		if leftDirectory != rightDirectory {
			return leftDirectory
		}
		return entries[left].Name() < entries[right].Name()
	})
	if len(entries) > remaining {
		loader.snapshot.TreeTruncated = true
		entries = entries[:remaining]
	} else if readErr == nil {
		// ReadDir(n) returning nil means EOF was not established. Peek once so
		// an exact-size directory does not force an unbounded second read.
		more, peekErr := directory.ReadDir(1)
		if len(more) > 0 {
			loader.snapshot.TreeTruncated = true
		}
		if peekErr != nil && !errors.Is(peekErr, io.EOF) {
			loader.recordError(artifactFilesystemError("read artifact directory", peekErr))
		}
	}

	for position, entry := range entries {
		if err := loader.ctx.Err(); err != nil {
			loader.snapshot.TreeTruncated = true
			loader.recordError(err.Error())
			return ""
		}
		if len(loader.snapshot.Tree) >= loader.limits.Entries {
			loader.snapshot.TreeTruncated = true
			return ""
		}

		rawName := entry.Name()
		rawRelative := filepath.Join(relative, rawName)
		item := artifactTreeEntry{
			Name:         strings.ToValidUTF8(rawName, "�"),
			RelativePath: filepath.ToSlash(strings.ToValidUTF8(rawRelative, "�")),
			Depth:        parentDepth + 1,
			Symlink:      entry.Type()&os.ModeSymlink != 0,
		}
		if !item.Symlink {
			item.Directory = entry.IsDir()
			if !item.Directory {
				entryInfo, infoErr := entry.Info()
				if infoErr != nil {
					item.Error = artifactFilesystemError("inspect artifact entry", infoErr)
					loader.recordError(item.Error)
				} else {
					item.Directory = entryInfo.IsDir()
					item.Regular = entryInfo.Mode().IsRegular()
				}
			}
		}
		loader.snapshot.Tree = append(loader.snapshot.Tree, item)
		index := len(loader.snapshot.Tree) - 1

		if !item.Directory || item.Symlink || item.Error != "" {
			continue
		}
		if item.Depth >= loader.limits.Depth || loader.openedDirs >= loader.limits.Directories {
			loader.snapshot.TreeTruncated = true
			continue
		}
		message := loader.walk(filepath.Join(path, rawName), rawRelative, item.Depth, false)
		if message != "" {
			loader.snapshot.Tree[index].Error = message
			loader.recordError(message)
		}
		if len(loader.snapshot.Tree) >= loader.limits.Entries && position < len(entries)-1 {
			loader.snapshot.TreeTruncated = true
			return ""
		}
	}
	return ""
}

func (loader *artifactTreeLoader) recordError(message string) {
	if message != "" && loader.snapshot.TreeError == "" {
		loader.snapshot.TreeError = message
	}
}

func artifactFilesystemError(action string, err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded.Error()
	case errors.Is(err, os.ErrNotExist):
		return action + ": missing"
	case errors.Is(err, os.ErrPermission):
		return action + ": permission denied"
	default:
		return strings.ToValidUTF8(fmt.Sprintf("%s: %v", action, err), "�")
	}
}
