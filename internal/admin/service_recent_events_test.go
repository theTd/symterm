package admin

import "testing"

func TestSummarizeRecentEventsCollapsesRepeatedSessionUpserts(t *testing.T) {
	t.Parallel()

	events := []Event{
		{
			Cursor: 1,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-1",
				ProjectID: "alpha",
			},
		},
		{
			Cursor: 2,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-1",
				ProjectID: "alpha",
			},
		},
		{
			Cursor: 3,
			Kind:   EventKindAuditAppended,
			Audit: &AuditRecord{
				Action: "create_user",
				Target: "alice",
			},
		},
		{
			Cursor: 4,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-1",
				ProjectID: "alpha",
			},
		},
		{
			Cursor: 5,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-2",
				ProjectID: "beta",
			},
		},
	}

	recent := summarizeRecentEvents(events, 20)
	if len(recent) != 3 {
		t.Fatalf("len(recent) = %d, want 3", len(recent))
	}
	if recent[0].Kind != EventKindAuditAppended || recent[0].Cursor != 3 {
		t.Fatalf("recent[0] = %#v, want audit cursor 3", recent[0])
	}
	if recent[1].Kind != EventKindSessionUpsert || recent[1].Cursor != 4 {
		t.Fatalf("recent[1] = %#v, want latest session-1 upsert cursor 4", recent[1])
	}
	if recent[2].Kind != EventKindSessionUpsert || recent[2].Cursor != 5 {
		t.Fatalf("recent[2] = %#v, want session-2 upsert cursor 5", recent[2])
	}
}

func TestSummarizeRecentEventsRespectsLimitAfterDeduping(t *testing.T) {
	t.Parallel()

	events := []Event{
		{Cursor: 1, Kind: EventKindDaemonUpdated},
		{
			Cursor: 2,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-1",
			},
		},
		{
			Cursor: 3,
			Kind:   EventKindSessionUpsert,
			Session: &SessionSnapshot{
				SessionID: "session-1",
			},
		},
		{
			Cursor: 4,
			Kind:   EventKindSessionClosed,
			Session: &SessionSnapshot{
				SessionID: "session-1",
			},
			SessionID: "session-1",
		},
	}

	recent := summarizeRecentEvents(events, 2)
	if len(recent) != 2 {
		t.Fatalf("len(recent) = %d, want 2", len(recent))
	}
	if recent[0].Cursor != 3 || recent[1].Cursor != 4 {
		t.Fatalf("recent cursors = [%d %d], want [3 4]", recent[0].Cursor, recent[1].Cursor)
	}
}
