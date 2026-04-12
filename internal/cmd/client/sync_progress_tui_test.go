package client

import (
	"bytes"
	"strings"
	"testing"

	"symterm/internal/proto"
)

func TestSyncProgressTUIRendersProgressAndOperations(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	tui := &syncProgressTUI{
		writer: &output,
	}
	observer := tui.InitialSyncObserver()
	if observer == nil {
		t.Fatal("observer is nil")
	}

	observer.Operation("Scanning workspace manifest")
	observer.Progress(proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseScanManifest,
		Completed: 1,
		Total:     2,
	})
	observer.Operation("Uploading internal/app/project_session_flow.go")
	tui.FinishInitialSync()

	rendered := output.String()
	for _, needle := range []string{
		"Syncing workspace",
		"Phase: Scan manifest 1/2",
		"Current: Uploading internal/app/project_session_flow.go",
		"Recent operations:",
		"  Scanning workspace manifest",
		"\x1b[2K",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered output missing %q:\n%s", needle, rendered)
		}
	}
}

func TestSyncOverallPercentUsesPhaseWeights(t *testing.T) {
	t.Parallel()

	progress := proto.SyncProgress{
		Phase:     proto.SyncProgressPhaseUploadFiles,
		Completed: 3,
		Total:     4,
	}
	if got := syncOverallPercent(progress); got != 78 {
		t.Fatalf("syncOverallPercent() = %d, want 78", got)
	}
}
