package main

import (
	"encoding/json"
	"testing"
)

// The shape a read of `_platform.accessibility` actually returns: the
// collection is keyed by field, so it is an array even with one record.
// Decoding it as a single object was the bug that kept "follow VoiceOver"
// off no matter what the shell reported.
func TestDecodeVoiceOverReadsTheArrayARealReadReturns(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`[{"id":"state","voice_over":true}]`, true},
		{`[{"id":"state","voice_over":false}]`, false},
		{`[]`, false},
		{`{"id":"state","voice_over":true}`, true},
		{`not json`, false},
	}
	for _, c := range cases {
		if got := decodeVoiceOver(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("decodeVoiceOver(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// A stored choice from the old `enabled` switch carries over.
func TestSpeakReadsTheOldSwitch(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		c    SpeechConfig
		want string
	}{
		{SpeechConfig{}, "no"},
		{SpeechConfig{Enabled: &yes}, "yes"},
		{SpeechConfig{Enabled: &no}, "no"},
		{SpeechConfig{Speak: "no", Enabled: &yes}, "no"},
	}
	for _, c := range cases {
		if got := c.c.speak(); got != c.want {
			t.Errorf("%+v -> %q, want %q", c.c, got, c.want)
		}
	}
}
