package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
)

const (
	maxReplayBytes       = 4 << 20
	maxReplayInputs      = 100000
	maxReplayTimeMS      = 86400000
	maxScoreRequestBytes = (8 << 20) + 8192
)

// Recordings are optional attachments. Invalid recordings must not discard an
// otherwise valid score. Canonicalize before hashing so invalid/null/absent
// attachments retain the pre-021 payload digest and old retry keys still work.
func normalizeScoreReplay(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > maxReplayBytes {
		return nil
	}
	var envelope struct {
		Version      *int            `json:"version"`
		AudioOffset  *int32          `json:"audio_offset_ms"`
		VisualOffset *int32          `json:"visual_offset_ms"`
		Inputs       json.RawMessage `json:"inputs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || envelope.Version == nil || *envelope.Version != 1 ||
		envelope.AudioOffset == nil || envelope.VisualOffset == nil {
		return nil
	}
	var events [][]json.RawMessage
	if json.Unmarshal(envelope.Inputs, &events) != nil || events == nil || len(events) > maxReplayInputs {
		return nil
	}
	inputs := make([][2]float64, 0, len(events))
	for _, event := range events {
		if len(event) != 2 {
			return nil
		}
		var ms *float64
		var key *int
		if json.Unmarshal(event[0], &ms) != nil || json.Unmarshal(event[1], &key) != nil ||
			ms == nil || key == nil || math.IsNaN(*ms) || math.IsInf(*ms, 0) ||
			math.Abs(*ms) > maxReplayTimeMS || *key < 0 || *key > 3 {
			return nil
		}
		// Preserve game consumption order. Hard audio resync can move game time
		// backwards, and multiple inputs in one frame share a timestamp.
		inputs = append(inputs, [2]float64{*ms, float64(*key)})
	}
	canonical, err := json.Marshal(inputs)
	if err != nil {
		return nil
	}
	envelope.Inputs = canonical
	canonical, err = json.Marshal(envelope)
	if err != nil || len(canonical) > maxReplayBytes {
		return nil
	}
	return canonical
}
