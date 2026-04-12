//go:build !linux

package daemon

import (
	"symterm/internal/proto"
)

func startFuseMount(_ proto.ProjectKey, _ ProjectLayout, _ ProjectFilesystem) (projectMountSession, error) {
	return nil, proto.NewError(proto.ErrProjectNotReady, "remote daemon requires linux + FUSE3")
}
