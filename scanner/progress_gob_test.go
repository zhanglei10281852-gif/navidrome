package scanner

import (
	"bytes"
	"encoding/gob"
	"testing"
)

// TestProgressInfoGobCompat ensures the external-scanner progress protocol stays
// compatible: older processes without Mode/PlanID/Stage decode fine, and the new
// fields survive a round-trip.
func TestProgressInfoGobCompat(t *testing.T) {
	// Round-trip with all fields set.
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	dec := gob.NewDecoder(&buf)

	orig := &ProgressInfo{
		LibID: 2, FileCount: 7, Path: "Rock", Phase: "1",
		ChangesDetected: true, Mode: PlanModePreview, PlanID: "plan-123", Stage: "classify",
	}
	if err := enc.Encode(orig); err != nil {
		t.Fatal(err)
	}
	var decoded ProgressInfo
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Mode != PlanModePreview || decoded.PlanID != "plan-123" || decoded.Stage != "classify" {
		t.Fatalf("plan fields lost after gob round-trip: %+v", decoded)
	}
	if decoded.LibID != 2 || decoded.FileCount != 7 || decoded.Path != "Rock" || decoded.Phase != "1" {
		t.Fatalf("core fields mismatch after gob round-trip: %+v", decoded)
	}

	// Legacy bytes (struct without the plan fields) must still decode.
	type legacyProgressInfo struct {
		LibID           int
		FileCount       uint32
		Path            string
		Phase           string
		ChangesDetected bool
		Warning         string
		Error           string
		ForceUpdate     bool
	}
	var legacy bytes.Buffer
	if err := gob.NewEncoder(&legacy).Encode(&legacyProgressInfo{LibID: 9, FileCount: 3, Phase: "2"}); err != nil {
		t.Fatal(err)
	}
	var modern ProgressInfo
	if err := gob.NewDecoder(&legacy).Decode(&modern); err != nil {
		t.Fatalf("legacy ProgressInfo bytes must decode: %v", err)
	}
	if modern.LibID != 9 || modern.FileCount != 3 || modern.ModernZeroMode() != "" {
		t.Fatalf("legacy decode mismatch: %+v", modern)
	}
}
