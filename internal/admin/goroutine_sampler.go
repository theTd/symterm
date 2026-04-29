package admin

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// goroutineSampleInterval is how often the sampler dumps every goroutine's
	// stack with runtime.Stack(buf, true). Each sample walks the entire process
	// so this is set conservatively rather than aggressively.
	goroutineSampleInterval = 30 * time.Second

	// goroutineStuckMinutes is the minimum wait time the runtime must report for
	// a goroutine to be considered stuck. runtime.Stack only prints the wait
	// time at minute resolution and only when the wait exceeds one minute, so
	// values below 1 are not observable here.
	goroutineStuckMinutes = 5

	// goroutineStackBufferSize bounds runtime.Stack output. 256KB comfortably
	// holds a few thousand goroutines at typical stack depths; if the daemon
	// exceeds that the tail is dropped, which the sampler tolerates.
	goroutineStackBufferSize = 256 * 1024

	// goroutineMaxFramesPerEvent caps how many frames each event carries so a
	// pathological deep stack cannot blow up admin event payloads.
	goroutineMaxFramesPerEvent = 32

	// goroutineMaxEventsPerScan is the per-scan emit ceiling that prevents a
	// flood of stuck goroutines from drowning the event stream.
	goroutineMaxEventsPerScan = 8
)

type GoroutineSnapshot struct {
	SampledAt   time.Time `json:"sampled_at"`
	GoID        string    `json:"go_id"`
	State       string    `json:"state"`
	WaitMinutes int       `json:"wait_minutes,omitempty"`
	Frames      []string  `json:"frames"`
	Fingerprint string    `json:"fingerprint"`
}

func cloneGoroutineSnapshot(snapshot GoroutineSnapshot) GoroutineSnapshot {
	snapshot.Frames = append([]string(nil), snapshot.Frames...)
	return snapshot
}

// StartGoroutineSampler runs a background sampler that emits goroutine_stuck
// events when goroutines remain in a wait state past goroutineStuckMinutes.
// The sampler stops when ctx is cancelled.
func (h *EventHub) StartGoroutineSampler(ctx context.Context) {
	if h == nil {
		return
	}
	go h.runGoroutineSampler(ctx)
}

func (h *EventHub) runGoroutineSampler(ctx context.Context) {
	ticker := time.NewTicker(goroutineSampleInterval)
	defer ticker.Stop()

	reported := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		seenNow := make(map[string]struct{})
		now := time.Now().UTC()
		emitted := 0

		records := captureGoroutineDump()
		for _, record := range records {
			if record.waitMinutes < goroutineStuckMinutes {
				continue
			}
			key := record.id + "|" + record.fingerprint
			seenNow[key] = struct{}{}
			if _, already := reported[key]; already {
				continue
			}
			if emitted >= goroutineMaxEventsPerScan {
				continue
			}
			reported[key] = struct{}{}
			emitted++
			h.recordGoroutineStuck(GoroutineSnapshot{
				SampledAt:   now,
				GoID:        record.id,
				State:       record.state,
				WaitMinutes: record.waitMinutes,
				Frames:      record.frames,
				Fingerprint: record.fingerprint,
			})
		}

		// Drop entries that no longer appear in the dump so a recycled goid that
		// blocks again later can be reported as a fresh stuck episode.
		for key := range reported {
			if _, ok := seenNow[key]; !ok {
				delete(reported, key)
			}
		}
	}
}

func (h *EventHub) recordGoroutineStuck(snapshot GoroutineSnapshot) {
	if h == nil {
		return
	}
	clone := cloneGoroutineSnapshot(snapshot)
	h.events.Append(Event{
		Kind:      EventKindGoroutineStuck,
		Goroutine: &clone,
	})
}

type goroutineRecord struct {
	id          string
	state       string
	waitMinutes int
	frames      []string
	fingerprint string
}

func captureGoroutineDump() []goroutineRecord {
	buf := make([]byte, goroutineStackBufferSize)
	n := runtime.Stack(buf, true)
	return parseGoroutineDump(string(buf[:n]))
}

func parseGoroutineDump(dump string) []goroutineRecord {
	var records []goroutineRecord
	for _, block := range strings.Split(dump, "\n\n") {
		record, ok := parseGoroutineBlock(block)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	return records
}

func parseGoroutineBlock(block string) (goroutineRecord, bool) {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "goroutine ") {
		return goroutineRecord{}, false
	}
	header := strings.TrimPrefix(lines[0], "goroutine ")
	spaceIdx := strings.IndexByte(header, ' ')
	if spaceIdx < 0 {
		return goroutineRecord{}, false
	}
	goid := header[:spaceIdx]
	rest := header[spaceIdx+1:]
	open := strings.IndexByte(rest, '[')
	closeIdx := strings.IndexByte(rest, ']')
	if open < 0 || closeIdx < 0 || closeIdx <= open {
		return goroutineRecord{}, false
	}
	bracket := rest[open+1 : closeIdx]
	state := bracket
	waitMinutes := 0
	if comma := strings.IndexByte(bracket, ','); comma >= 0 {
		state = strings.TrimSpace(bracket[:comma])
		waitMinutes = parseLeadingMinutes(strings.TrimSpace(bracket[comma+1:]))
	} else {
		state = strings.TrimSpace(bracket)
	}
	var frames []string
	for i := 1; i < len(lines) && len(frames) < goroutineMaxFramesPerEvent; i += 2 {
		fn := strings.TrimSpace(lines[i])
		if fn == "" {
			break
		}
		frames = append(frames, fn)
	}
	if len(frames) == 0 {
		return goroutineRecord{}, false
	}
	return goroutineRecord{
		id:          goid,
		state:       state,
		waitMinutes: waitMinutes,
		frames:      frames,
		fingerprint: fingerprintFrames(frames),
	}, true
}

func parseLeadingMinutes(text string) int {
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	value, err := strconv.Atoi(text[:end])
	if err != nil {
		return 0
	}
	if !strings.Contains(text[end:], "minute") {
		return 0
	}
	return value
}

func fingerprintFrames(frames []string) string {
	hash := sha1.New()
	for _, frame := range frames {
		hash.Write([]byte(frame))
		hash.Write([]byte{'\n'})
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum[:8])
}
