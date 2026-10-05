package main

import (
	"reflect"
	"testing"

	"github.com/branchkit/plugin-sdk-go"
)

type fakeVoice struct {
	log       []string
	voiceOver bool
}

func (f *fakeVoice) Say(text string, high bool) {
	if high {
		f.log = append(f.log, "stop")
	}
	f.log = append(f.log, "say:"+text)
}
func (f *fakeVoice) Stop()                { f.log = append(f.log, "stop") }
func (f *fakeVoice) UseVoiceOver(on bool) { f.voiceOver = on }

func doc(kind, title, phrase, urgency string, items ...string) branchkit.OutputState {
	d := branchkit.OutputState{Kind: kind, Title: title, Phrase: phrase, Urgency: urgency}
	if len(items) > 0 {
		s := branchkit.OutputSection{Title: "Commands"}
		for _, it := range items {
			s.Items = append(s.Items, branchkit.OutputItem{ID: it, Title: it, Phrase: it})
		}
		d.Sections = []branchkit.OutputSection{s}
	}
	return d
}

func state(channel string, gen int, changed bool, d branchkit.OutputState) branchkit.OutputStateEventParams {
	return branchkit.OutputStateEventParams{Channel: channel, PluginID: "voice", Generation: gen, MeaningChanged: changed, State: d}
}

func renderer(t *testing.T, p Policy) (*Renderer, *fakeVoice) {
	t.Helper()
	v := &fakeVoice{}
	r := NewRenderer(v)
	r.Apply(p)
	return r, v
}

var on = Policy{Enabled: true, ChoicesLimit: 6}

func says(v *fakeVoice) []string {
	var out []string
	for _, l := range v.log {
		if len(l) > 4 && l[:4] == "say:" {
			out = append(out, l)
		}
	}
	return out
}

// --- policy ---

func TestProgressIsNeverWords(t *testing.T) {
	if _, ok := Utterance(state("d", 1, true, doc("progress", "Loading", "warming up", "ambient")), on); ok {
		t.Fatal("progress must be silent")
	}
}

func TestProblemsAlwaysSpeakOutcomesOnlyWhenAsked(t *testing.T) {
	got, _ := Utterance(state("d", 2, true, doc("problem", "Couldn't type that", "Grant Accessibility in System Settings", "interrupt")), on)
	if got != "Couldn't type that. Grant Accessibility in System Settings" {
		t.Fatalf("problem: %q", got)
	}
	if _, ok := Utterance(state("d", 1, true, doc("outcome", "", "snap left", "notable")), on); ok {
		t.Fatal("an outcome is the person's own words read back; off unless asked")
	}
	got, ok := Utterance(state("d", 1, true, doc("outcome", "", "snap left", "notable")), Policy{Enabled: true, SpeakOutcomes: true, ChoicesLimit: 6})
	if !ok || got != "snap left" {
		t.Fatalf("asked-for outcome: %q %v", got, ok)
	}
}

// A notification is addressed to the person, not their own command read
// back: a sighted person who chose "Speak announcements: Always" hears it
// though "Say what happened" is off: notifications were once published as
// outcomes, and outcomes are spoken only with that switch on.
func TestNoticesSpeakWheneverSpeakingIsOn(t *testing.T) {
	sighted := DerivePolicy(Profile{Speak: "yes", Sight: "yes", Hearing: "yes", ChoicesLimit: 6}, false)
	if !sighted.Enabled || sighted.SpeakOutcomes {
		t.Fatalf("precondition: speaking on, outcomes off: %+v", sighted)
	}
	got, ok := Utterance(state("n", 1, true, doc("notice", "Download finished", "The model is ready", "notable")), sighted)
	if !ok || got != "Download finished. The model is ready" {
		t.Fatalf("notice: %q %v", got, ok)
	}
	if got, ok := Utterance(state("n", 2, true, doc("notice", "", "Back online", "notable")), sighted); !ok || got != "Back online" {
		t.Fatalf("phrase-only notice: %q %v", got, ok)
	}
	if _, ok := Utterance(state("n", 3, true, doc("outcome", "", "snap left", "notable")), sighted); ok {
		t.Fatal("an outcome stays quiet under the same policy")
	}
}

func TestChoicesReadPhraseThenLimitedItems(t *testing.T) {
	d := doc("choices", "Snap mode", "snap left. 6 commands", "ambient", "left", "right", "center", "full", "next", "previous")
	got, _ := Utterance(state("d", 1, true, d), Policy{Enabled: true, ChoicesLimit: 3})
	if got != "snap left. 6 commands: left, right, center, and 3 more" {
		t.Fatalf("limit 3: %q", got)
	}
	got, _ = Utterance(state("d", 1, true, d), on)
	if got != "snap left. 6 commands: left, right, center, full, next, previous" {
		t.Fatalf("limit 6: %q", got)
	}
	got, _ = Utterance(state("c", 1, true, doc("choices", "", "", "ambient", "a", "b")), on)
	if got != "2 choices: a, b" {
		t.Fatalf("no phrase: %q", got)
	}
}

func TestSameMeaningAndUnknownKind(t *testing.T) {
	if _, ok := Utterance(state("d", 5, false, doc("choices", "", "6 commands", "ambient", "left")), on); ok {
		t.Fatal("same meaning says nothing")
	}
	got, _ := Utterance(state("x", 1, true, doc("future_kind", "T", "the phrase", "ambient")), on)
	if got != "the phrase" {
		t.Fatalf("unknown kind degrades to the phrase, got %q", got)
	}
}

// --- behaviour ---

func TestDisabledSpeaksNothing(t *testing.T) {
	r, v := renderer(t, Policy{})
	r.HandleState(state("d", 1, true, doc("problem", "", "nope", "interrupt")))
	if len(v.log) != 0 {
		t.Fatalf("disabled: %v", v.log)
	}
}

func TestSilentWhileAKeyIsHeldThenSpeaksNewestAtTheBoundary(t *testing.T) {
	r, v := renderer(t, on)
	r.PipelineStarted("command_recognition", true)
	r.HandleState(state("d", 1, true, doc("choices", "", "6 commands", "ambient", "left")))
	r.HandleState(state("d", 2, true, doc("choices", "", "snap left. 6 commands", "ambient", "left")))
	if len(says(v)) != 0 {
		t.Fatalf("held: %v", v.log)
	}
	r.PipelineStopped("command_recognition")
	if got := says(v); !reflect.DeepEqual(got, []string{"say:snap left. 6 commands: left"}) {
		t.Fatalf("boundary: %v", got)
	}
}

func TestAPersistentPipelineDoesNotSilenceSpeechButAHoldCutsIt(t *testing.T) {
	r, v := renderer(t, on)
	r.PipelineStarted("command_recognition", false)
	r.HandleState(state("d", 1, true, doc("problem", "", "mic lost", "interrupt")))
	if !reflect.DeepEqual(v.log, []string{"say:mic lost"}) {
		t.Fatalf("continuous: %v", v.log)
	}
	r.PipelineStarted("dictation", true)
	if v.log[len(v.log)-1] != "stop" {
		t.Fatalf("a hold cuts speech: %v", v.log)
	}
	r.HandleState(state("d", 2, true, doc("problem", "", "still lost", "interrupt")))
	r.PipelineStopped("dictation")
	if v.log[len(v.log)-1] != "say:still lost" {
		t.Fatalf("held through the hold: %v", v.log)
	}
}

func TestNewerGenerationSupersedesAndStaleIsIgnored(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("d", 1, true, doc("choices", "", "6 commands", "ambient", "left")))
	r.HandleState(state("d", 2, true, doc("problem", "", "gone wrong", "interrupt")))
	if !reflect.DeepEqual(v.log, []string{"say:6 commands: left", "stop", "say:gone wrong"}) {
		t.Fatalf("supersede: %v", v.log)
	}
	r.HandleState(state("d", 1, true, doc("problem", "", "older", "interrupt")))
	if v.log[len(v.log)-1] != "say:gone wrong" {
		t.Fatalf("stale must not speak: %v", v.log)
	}
}

func TestAmbientQueuesNotableCutsInAcrossChannels(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("d", 1, true, doc("choices", "", "6 commands", "ambient", "left")))
	r.HandleState(state("n", 1, true, doc("mode", "", "quiet mode", "ambient")))
	if !reflect.DeepEqual(v.log, []string{"say:6 commands: left", "say:quiet mode"}) {
		t.Fatalf("ambient queues: %v", v.log)
	}
	r.HandleState(state("r", 1, true, doc("problem", "Mic lost", "Reconnect it", "interrupt")))
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:Mic lost. Reconnect it"}) {
		t.Fatalf("interrupt cuts in: %v", got)
	}
}

func TestClearStopsOnlyItsOwnChannelAndDropsAHeldDocument(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("d", 1, true, doc("problem", "", "gone wrong", "interrupt")))
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "n", PluginID: "voice", Generation: 9})
	if !reflect.DeepEqual(v.log, []string{"say:gone wrong"}) {
		t.Fatalf("another channel's clear: %v", v.log)
	}
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "d", PluginID: "voice", Generation: 2})
	if v.log[len(v.log)-1] != "stop" {
		t.Fatalf("own clear stops: %v", v.log)
	}
	r.PipelineStarted("command_recognition", true)
	r.HandleState(state("d", 3, true, doc("choices", "", "6 commands", "ambient", "left")))
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "d", PluginID: "voice", Generation: 4})
	r.PipelineStopped("command_recognition")
	if len(says(v)) != 1 {
		t.Fatalf("a cleared held document is not spoken at the boundary: %v", v.log)
	}
}

func TestTurningSpeechOffStopsMidSentence(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("d", 1, true, doc("problem", "", "gone wrong", "interrupt")))
	r.Apply(Policy{})
	if v.log[len(v.log)-1] != "stop" {
		t.Fatalf("off stops: %v", v.log)
	}
	r.HandleState(state("d", 2, true, doc("problem", "", "again", "interrupt")))
	if v.log[len(v.log)-1] != "stop" {
		t.Fatalf("off stays silent: %v", v.log)
	}
}

// --- the profile ---

func TestProfileConfiguresThePersonNotTheSounds(t *testing.T) {
	base := Profile{Speak: "yes", ChoicesLimit: 6, Sight: "system", Hearing: "yes"}
	// Sighted, VoiceOver off: problems, modes and choices; not their own words back.
	if p := DerivePolicy(base, false); !p.Enabled || p.SpeakOutcomes || p.UseVoiceOver {
		t.Fatalf("sighted default: %+v", p)
	}
	// Following VoiceOver and it is on: they are not reading the screen —
	// what happened is spoken, through VoiceOver.
	if p := DerivePolicy(base, true); !p.SpeakOutcomes || !p.UseVoiceOver {
		t.Fatalf("voiceover on: %+v", p)
	}
	// Said "yes, I can see" — VoiceOver on changes nothing.
	sees := base
	sees.Sight = "yes"
	if p := DerivePolicy(sees, true); p.SpeakOutcomes || p.UseVoiceOver {
		t.Fatalf("can see, VoiceOver on: %+v", p)
	}
	// Said "no, I cannot see" with VoiceOver off: outcomes spoken by the system voice.
	blind := base
	blind.Sight = "no"
	if p := DerivePolicy(blind, false); !p.SpeakOutcomes || p.UseVoiceOver {
		t.Fatalf("cannot see, no VoiceOver: %+v", p)
	}
	// Cannot hear: the switch is inert.
	deaf := base
	deaf.Hearing = "no"
	if p := DerivePolicy(deaf, true); p.Enabled {
		t.Fatalf("cannot hear: %+v", p)
	}
	// Speak "system" (the default) follows what they cannot see: silent for
	// a sighted person, on when VoiceOver is.
	follows := base
	follows.Speak = "system"
	if p := DerivePolicy(follows, false); p.Enabled {
		t.Fatalf("speak=system, sighted: %+v", p)
	}
	if p := DerivePolicy(follows, true); !p.Enabled || !p.UseVoiceOver {
		t.Fatalf("speak=system, VoiceOver on: %+v", p)
	}
	never := base
	never.Speak = "no"
	if p := DerivePolicy(never, true); p.Enabled {
		t.Fatalf("speak=no: %+v", p)
	}
	// The override still turns outcomes on for a sighted person who wants them.
	wants := base
	wants.SpeakOutcomes = true
	if p := DerivePolicy(wants, false); !p.SpeakOutcomes {
		t.Fatalf("override: %+v", p)
	}
}

func TestApplyRoutesTheVoiceThroughVoiceOverWhenThePolicySaysSo(t *testing.T) {
	r, v := renderer(t, DerivePolicy(Profile{Speak: "yes", ChoicesLimit: 6, Sight: "system", Hearing: "yes"}, true))
	if !v.voiceOver {
		t.Fatal("voice should route through VoiceOver")
	}
	r.Apply(DerivePolicy(Profile{Speak: "yes", ChoicesLimit: 6, Sight: "yes", Hearing: "yes"}, true))
	if v.voiceOver {
		t.Fatal("a person who can see keeps the system voice")
	}
}
