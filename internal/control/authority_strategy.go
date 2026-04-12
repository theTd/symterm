package control

import "strings"

func sessionUsesWorkspaceRootAuthority(clientSession session) bool {
	return strings.TrimSpace(clientSession.WorkspaceRoot) != "" && clientSession.TransportHint != TransportKindSSH
}

func sessionUsesOwnerFileAuthority(clientSession session) bool {
	return !sessionUsesWorkspaceRootAuthority(clientSession)
}
