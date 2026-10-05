package main

import (
	"encoding/json"
	"os"
	"testing"
)

// Every voice the picker offers is one the manifest ships: a stage to speak
// with (all but the system voice) and, for a downloadable one, the artifact
// that stage loads. A voice added to one list and not the other would offer
// something that can never speak.
func TestEveryOfferedVoiceIsDeclaredInTheManifest(t *testing.T) {
	raw, err := os.ReadFile("../plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Provides struct {
			Stages map[string]struct {
				StageType string   `json:"stage_type"`
				Args      []string `json:"args"`
			} `json:"stages"`
			Artifacts map[string]json.RawMessage `json:"artifacts"`
		} `json:"provides"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, c := range engineChoices {
		if c.key == "system" {
			continue
		}
		st, ok := m.Provides.Stages[c.key]
		if !ok || st.StageType != "tts" {
			t.Errorf("%s: no tts stage %q in provides.stages", c.label, c.key)
			continue
		}
		if c.artifact == "" {
			continue
		}
		if _, ok := m.Provides.Artifacts[c.artifact]; !ok {
			t.Errorf("%s: artifact %q not declared", c.label, c.artifact)
		}
		model := ""
		for i, a := range st.Args {
			if a == "--model" && i+1 < len(st.Args) {
				model = st.Args[i+1]
			}
		}
		if model != c.artifact {
			t.Errorf("%s: stage loads %q, the picker downloads %q", c.label, model, c.artifact)
		}
	}
}

func TestAnUnknownEngineFallsBackToTheSystemVoice(t *testing.T) {
	if choiceFor("no-such-voice").key != "system" {
		t.Fatal("an unknown engine setting must not pick a neural voice")
	}
	if choiceFor("kitten").artifact == "" {
		t.Fatal("kitten downloads its model")
	}
}
