package invalidation

import (
	"path/filepath"

	"symterm/internal/proto"
)

func DataPath(path string) []proto.InvalidateChange {
	return AppendChanges(nil,
		proto.InvalidateChange{Path: path, Kind: proto.InvalidateData},
		proto.InvalidateChange{Path: parentPath(path), Kind: proto.InvalidateDentry},
	)
}

func MkdirPath(path string) []proto.InvalidateChange {
	return AppendChanges(nil,
		proto.InvalidateChange{Path: path, Kind: proto.InvalidateDentry},
		proto.InvalidateChange{Path: parentPath(path), Kind: proto.InvalidateDentry},
	)
}

func DeletePath(path string) []proto.InvalidateChange {
	return AppendChanges(nil,
		proto.InvalidateChange{Path: path, Kind: proto.InvalidateDelete},
		proto.InvalidateChange{Path: parentPath(path), Kind: proto.InvalidateDentry},
	)
}

func RenamePaths(path string, newPath string) []proto.InvalidateChange {
	return AppendChanges(nil,
		proto.InvalidateChange{Path: path, NewPath: newPath, Kind: proto.InvalidateRename},
		proto.InvalidateChange{Path: parentPath(path), Kind: proto.InvalidateDentry},
		proto.InvalidateChange{Path: parentPath(newPath), Kind: proto.InvalidateDentry},
	)
}

func AppendChanges(existing []proto.InvalidateChange, candidates ...proto.InvalidateChange) []proto.InvalidateChange {
	for _, candidate := range candidates {
		duplicate := false
		for _, current := range existing {
			if current == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, candidate)
		}
	}
	return existing
}

func parentPath(path string) string {
	dir := filepath.Dir(filepath.ToSlash(path))
	if dir == "." {
		return ""
	}
	return filepath.ToSlash(dir)
}
