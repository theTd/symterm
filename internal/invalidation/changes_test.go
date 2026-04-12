package invalidation

import (
	"reflect"
	"testing"

	"symterm/internal/proto"
)

func TestDataPathIncludesParentDentry(t *testing.T) {
	t.Parallel()

	got := DataPath("dir/file.txt")
	want := []proto.InvalidateChange{
		{Path: "dir/file.txt", Kind: proto.InvalidateData},
		{Path: "dir", Kind: proto.InvalidateDentry},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DataPath() = %#v, want %#v", got, want)
	}
}

func TestMkdirPathDeduplicatesRootParent(t *testing.T) {
	t.Parallel()

	got := MkdirPath("child")
	want := []proto.InvalidateChange{
		{Path: "child", Kind: proto.InvalidateDentry},
		{Path: "", Kind: proto.InvalidateDentry},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MkdirPath() = %#v, want %#v", got, want)
	}
}

func TestRenamePathsDeduplicatesSharedParent(t *testing.T) {
	t.Parallel()

	got := RenamePaths("dir/old.txt", "dir/new.txt")
	want := []proto.InvalidateChange{
		{Path: "dir/old.txt", NewPath: "dir/new.txt", Kind: proto.InvalidateRename},
		{Path: "dir", Kind: proto.InvalidateDentry},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RenamePaths() = %#v, want %#v", got, want)
	}
}

func TestAppendChangesKeepsFirstOccurrence(t *testing.T) {
	t.Parallel()

	initial := []proto.InvalidateChange{
		{Path: "dir", Kind: proto.InvalidateDentry},
	}
	got := AppendChanges(initial,
		proto.InvalidateChange{Path: "dir", Kind: proto.InvalidateDentry},
		proto.InvalidateChange{Path: "dir/file.txt", Kind: proto.InvalidateData},
	)
	want := []proto.InvalidateChange{
		{Path: "dir", Kind: proto.InvalidateDentry},
		{Path: "dir/file.txt", Kind: proto.InvalidateData},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AppendChanges() = %#v, want %#v", got, want)
	}
}
