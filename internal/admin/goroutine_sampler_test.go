package admin

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseGoroutineDumpExtractsStateAndWait(t *testing.T) {
	t.Parallel()

	dump := strings.Join([]string{
		"goroutine 1 [running]:",
		"main.main()",
		"\t/repo/main.go:10 +0x10",
		"",
		"goroutine 42 [chan receive, 7 minutes]:",
		"main.worker(...)",
		"\t/repo/worker.go:42 +0x20",
		"main.runner()",
		"\t/repo/runner.go:8 +0x30",
		"",
	}, "\n")

	records := parseGoroutineDump(dump)
	if len(records) != 2 {
		t.Fatalf("parseGoroutineDump() len = %d, want 2", len(records))
	}

	first := records[0]
	if first.id != "1" || first.state != "running" || first.waitMinutes != 0 {
		t.Fatalf("first record = %#v", first)
	}
	if len(first.frames) != 1 || first.frames[0] != "main.main()" {
		t.Fatalf("first.frames = %#v", first.frames)
	}

	second := records[1]
	if second.id != "42" || second.state != "chan receive" || second.waitMinutes != 7 {
		t.Fatalf("second record = %#v", second)
	}
	if len(second.frames) != 2 || second.frames[0] != "main.worker(...)" || second.frames[1] != "main.runner()" {
		t.Fatalf("second.frames = %#v", second.frames)
	}
	if second.fingerprint == "" {
		t.Fatalf("second.fingerprint is empty")
	}
}

func TestParseGoroutineDumpIgnoresMalformedBlocks(t *testing.T) {
	t.Parallel()

	dump := strings.Join([]string{
		"not a goroutine block",
		"\t/repo/random.go:1",
		"",
		"goroutine 5 [select]:",
		"",
		"goroutine 9 [select]:",
		"main.idle()",
		"\t/repo/idle.go:1 +0x1",
		"",
	}, "\n")

	records := parseGoroutineDump(dump)
	if len(records) != 1 {
		t.Fatalf("parseGoroutineDump() len = %d, want 1 (only well-formed block)", len(records))
	}
	if records[0].id != "9" || records[0].state != "select" {
		t.Fatalf("record = %#v", records[0])
	}
}

func TestParseGoroutineDumpTruncatesFrames(t *testing.T) {
	t.Parallel()

	var lines []string
	lines = append(lines, "goroutine 7 [select, 12 minutes]:")
	for i := 0; i < goroutineMaxFramesPerEvent+5; i++ {
		lines = append(lines, "main.deep()")
		lines = append(lines, "\t/repo/deep.go:1 +0x1")
	}
	lines = append(lines, "")

	records := parseGoroutineDump(strings.Join(lines, "\n"))
	if len(records) != 1 {
		t.Fatalf("parseGoroutineDump() len = %d, want 1", len(records))
	}
	if got := len(records[0].frames); got != goroutineMaxFramesPerEvent {
		t.Fatalf("frame count = %d, want %d", got, goroutineMaxFramesPerEvent)
	}
}

func TestParseLeadingMinutesRejectsNonMinuteUnits(t *testing.T) {
	t.Parallel()

	cases := map[string]int{
		"3 minutes":   3,
		"1 minute":    1,
		"45 seconds":  0,
		"":            0,
		"abc minutes": 0,
		"15 minutes,": 15,
	}
	for input, want := range cases {
		got := parseLeadingMinutes(input)
		if got != want {
			t.Fatalf("parseLeadingMinutes(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestFingerprintFramesIsStableAndDifferentiates(t *testing.T) {
	t.Parallel()

	a := fingerprintFrames([]string{"main.foo()", "main.bar()"})
	b := fingerprintFrames([]string{"main.foo()", "main.bar()"})
	if a != b {
		t.Fatalf("fingerprintFrames stable: %s != %s", a, b)
	}
	c := fingerprintFrames([]string{"main.foo()", "main.baz()"})
	if a == c {
		t.Fatalf("fingerprintFrames(differing frames) collided: %s == %s", a, c)
	}
}

func TestCaptureGoroutineDumpReturnsCurrentGoroutine(t *testing.T) {
	t.Parallel()

	records := captureGoroutineDump()
	if len(records) == 0 {
		t.Fatalf("captureGoroutineDump() returned no records")
	}
	// The current goroutine should be in [running] state with at least one frame.
	foundRunning := false
	for _, record := range records {
		if record.state == "running" && len(record.frames) > 0 {
			foundRunning = true
			break
		}
	}
	if !foundRunning {
		t.Fatalf("captureGoroutineDump() missing a running goroutine; records = %d", len(records))
	}
	// Sanity: the runtime version we tested against still emits the expected format.
	if !strings.HasPrefix(runtime.Version(), "go") {
		t.Fatalf("unexpected runtime version: %s", runtime.Version())
	}
}
