package client

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"

	"symterm/internal/app"
	"symterm/internal/proto"
	workspacesync "symterm/internal/sync"
)

const syncProgressRecentLines = 4

type syncProgressTUI struct {
	mu sync.Mutex

	writer io.Writer
	file   *os.File

	progress      proto.SyncProgress
	operations    []string
	renderedLines int
	finished      bool
}

func newInitialSyncFeedback(stdout io.Writer) app.SyncFeedback {
	file, ok := stdout.(*os.File)
	if !ok || file == nil || !isTerminalDevice(file) {
		return nil
	}
	if !enableVirtualTerminalSequences(file) {
		return nil
	}
	return &syncProgressTUI{
		writer: stdout,
		file:   file,
	}
}

func (t *syncProgressTUI) InitialSyncObserver() *workspacesync.InitialSyncObserver {
	if t == nil {
		return nil
	}
	return &workspacesync.InitialSyncObserver{
		OnOperation: t.recordOperation,
		OnProgress:  t.recordProgress,
	}
}

func (t *syncProgressTUI) FinishInitialSync() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.clearLocked()
	t.finished = true
}

func (t *syncProgressTUI) recordOperation(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if len(t.operations) == 0 || t.operations[len(t.operations)-1] != message {
		t.operations = append(t.operations, message)
		if len(t.operations) > syncProgressRecentLines {
			t.operations = append([]string(nil), t.operations[len(t.operations)-syncProgressRecentLines:]...)
		}
	}
	t.renderLocked()
}

func (t *syncProgressTUI) recordProgress(progress proto.SyncProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.progress = progress
	t.renderLocked()
}

func (t *syncProgressTUI) applyRemoteStatus(progress *proto.SyncProgress, operations []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	if progress != nil {
		t.progress = *progress
	}
	if len(operations) > 0 {
		t.operations = append([]string(nil), operations...)
	}
	t.renderLocked()
}

func (t *syncProgressTUI) renderLocked() {
	lines := t.renderLinesLocked()
	if len(lines) == 0 {
		return
	}
	if t.renderedLines > 0 {
		t.rewindLocked(t.renderedLines)
		t.clearLineBlockLocked(t.renderedLines)
		if t.renderedLines > 1 {
			fmt.Fprintf(t.writer, "\x1b[%dF", t.renderedLines-1)
		} else {
			io.WriteString(t.writer, "\r")
		}
	}
	for _, line := range lines {
		io.WriteString(t.writer, "\x1b[2K")
		io.WriteString(t.writer, line)
		io.WriteString(t.writer, "\n")
	}
	t.renderedLines = len(lines)
}

func (t *syncProgressTUI) clearLocked() {
	if t.renderedLines == 0 {
		return
	}
	t.rewindLocked(t.renderedLines)
	t.clearLineBlockLocked(t.renderedLines)
	if t.renderedLines > 1 {
		fmt.Fprintf(t.writer, "\x1b[%dF", t.renderedLines-1)
	} else {
		io.WriteString(t.writer, "\r")
	}
	t.renderedLines = 0
}

func (t *syncProgressTUI) rewindLocked(lines int) {
	if lines <= 0 {
		return
	}
	fmt.Fprintf(t.writer, "\x1b[%dF", lines)
}

func (t *syncProgressTUI) clearLineBlockLocked(lines int) {
	for idx := 0; idx < lines; idx++ {
		io.WriteString(t.writer, "\x1b[2K")
		if idx < lines-1 {
			io.WriteString(t.writer, "\x1b[1E")
		}
	}
}

func (t *syncProgressTUI) renderLinesLocked() []string {
	width := t.terminalWidthLocked()
	overallPercent := syncOverallPercent(t.progress)
	phasePercent := syncPhasePercent(t.progress)
	phaseLabel := humanizeSyncPhase(t.progress.Phase)
	progressLine := fitTerminalLine(
		fmt.Sprintf("Syncing workspace %3d%% [%s]", overallPercent, renderProgressBar(progressBarWidth(width), phasePercent)),
		width,
	)
	phaseLine := fitTerminalLine("Phase: "+formatPhaseDetail(t.progress, phaseLabel), width)
	currentLine := fitTerminalLine("Current: "+t.currentOperationLocked(), width)

	lines := []string{progressLine, phaseLine, currentLine}
	if len(t.operations) > 1 {
		lines = append(lines, fitTerminalLine("Recent operations:", width))
		for _, op := range t.operations[:len(t.operations)-1] {
			lines = append(lines, fitTerminalLine("  "+op, width))
		}
	}
	return lines
}

func (t *syncProgressTUI) currentOperationLocked() string {
	if len(t.operations) == 0 {
		return "Waiting for sync operations"
	}
	return t.operations[len(t.operations)-1]
}

func (t *syncProgressTUI) terminalWidthLocked() int {
	if t.file != nil {
		if width, _, err := term.GetSize(int(t.file.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

func progressBarWidth(width int) int {
	switch {
	case width >= 120:
		return 32
	case width >= 96:
		return 24
	case width >= 72:
		return 18
	default:
		return 12
	}
}

func renderProgressBar(width int, percent int) string {
	if width <= 0 {
		width = 12
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := width * percent / 100
	if filled > width {
		filled = width
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

func formatPhaseDetail(progress proto.SyncProgress, phaseLabel string) string {
	if phaseLabel == "" {
		phaseLabel = "Waiting"
	}
	if progress.Total == 0 {
		return phaseLabel
	}
	return fmt.Sprintf("%s %d/%d", phaseLabel, progress.Completed, progress.Total)
}

func humanizeSyncPhase(phase proto.SyncProgressPhase) string {
	switch phase {
	case proto.SyncProgressPhaseBegin:
		return "Starting"
	case proto.SyncProgressPhaseScanManifest:
		return "Scan manifest"
	case proto.SyncProgressPhaseHashManifest:
		return "Hash manifest"
	case proto.SyncProgressPhaseUploadFiles:
		return "Upload files"
	case proto.SyncProgressPhaseFinalize:
		return "Finalize"
	default:
		return "Waiting"
	}
}

func syncOverallPercent(progress proto.SyncProgress) int {
	phasePercent := syncPhasePercent(progress)
	switch progress.Phase {
	case proto.SyncProgressPhaseBegin:
		return interpolatePercent(0, 5, phasePercent)
	case proto.SyncProgressPhaseScanManifest:
		return interpolatePercent(5, 30, phasePercent)
	case proto.SyncProgressPhaseHashManifest:
		return interpolatePercent(30, 45, phasePercent)
	case proto.SyncProgressPhaseUploadFiles:
		return interpolatePercent(45, 90, phasePercent)
	case proto.SyncProgressPhaseFinalize:
		return interpolatePercent(90, 100, phasePercent)
	default:
		return phasePercent
	}
}

func syncPhasePercent(progress proto.SyncProgress) int {
	if progress.Percent != nil {
		return clampPercent(*progress.Percent)
	}
	if progress.Total == 0 {
		return 100
	}
	return clampPercent(int((progress.Completed*100 + progress.Total/2) / progress.Total))
}

func interpolatePercent(start int, end int, percent int) int {
	if end < start {
		end = start
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return start + (end-start)*percent/100
}

func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func fitTerminalLine(line string, width int) string {
	if width <= 1 {
		return line
	}
	limit := width - 1
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func isTerminalDevice(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
