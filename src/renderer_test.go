package main

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/branchkit/plugin-sdk-go"
)

type fakeVoice struct {
	log       []string
	voiceOver bool
	route     string
	ids       map[string]string // text -> the id of its newest Say
	n         int
}

func (f *fakeVoice) Say(text string, high bool) string {
	if high {
		f.log = append(f.log, "stop")
	}
	f.log = append(f.log, "say:"+text)
	f.n++
	id := fmt.Sprintf("u%d", f.n)
	if f.ids == nil {
		f.ids = map[string]string{}
	}
	f.ids[text] = id
	return id
}

// finish reports the newest utterance of text as over, as the platform's
// speech.finished would.
func (f *fakeVoice) finish(r *Renderer, text string) { r.SpeechFinished(f.ids[text]) }

func (f *fakeVoice) Route() string        { return f.route }
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

// state is the event as the platform sends it for a plugin that declares
// its output may interrupt (voice does): a document that asks to interrupt
// (urgency interrupt, or a problem) is granted it.
func state(channel string, gen int, changed bool, d branchkit.OutputState) branchkit.OutputStateEventParams {
	attention := branchkit.OutputAttentionPolite
	if d.Urgency == "interrupt" || d.Kind == "problem" {
		attention = branchkit.OutputAttentionInterrupt
	}
	return branchkit.OutputStateEventParams{Channel: channel, PluginID: "voice", Generation: gen, MeaningChanged: changed, State: d, Attention: attention}
}

// undeclared is the same event from a plugin that does not declare it:
// whatever the document asks for, the platform answers polite.
func undeclared(msg branchkit.OutputStateEventParams) branchkit.OutputStateEventParams {
	msg.PluginID = "other"
	msg.Attention = branchkit.OutputAttentionPolite
	return msg
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

func TestAmbientQueuesInterruptCutsInAcrossChannels(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("d", 1, true, doc("choices", "", "6 commands", "ambient", "left")))
	r.HandleState(state("n", 1, true, doc("mode", "", "quiet mode", "ambient")))
	if !reflect.DeepEqual(v.log, []string{"say:6 commands: left", "say:quiet mode"}) {
		t.Fatalf("ambient queues: %v", v.log)
	}
	r.SpeechStarted(v.ids["6 commands: left"])
	// The interrupt cuts the choices list; "quiet mode", which was waiting
	// and never heard, is said again after it.
	r.HandleState(state("r", 1, true, doc("problem", "Mic lost", "Reconnect it", "interrupt")))
	if got := v.log[len(v.log)-3:]; !reflect.DeepEqual(got, []string{"stop", "say:Mic lost. Reconnect it", "say:quiet mode"}) {
		t.Fatalf("interrupt cuts in, what waited is said after: %v", got)
	}
}

// Notable is news, not an alarm: on another channel it waits its turn, and
// only interrupt cuts into what is playing.
func TestNotableWaitsOnAnotherChannelOnlyInterruptCutsIn(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "Download finished", "notable")))
	r.HandleState(state("b", 1, true, doc("notice", "", "Back online", "notable")))
	if !reflect.DeepEqual(v.log, []string{"say:Download finished", "say:Back online"}) {
		t.Fatalf("notable on another channel waits: %v", v.log)
	}
	r.SpeechStarted(v.ids["Download finished"])
	r.HandleState(state("c", 1, true, doc("problem", "", "Disk full", "interrupt")))
	if got := v.log[len(v.log)-3:]; !reflect.DeepEqual(got, []string{"stop", "say:Disk full", "say:Back online"}) {
		t.Fatalf("interrupt cuts in: %v", got)
	}
}

// The cut-in reads the platform's attention, never the document's own
// urgency or kind: an interrupt or a problem from a plugin that does not
// declare it waits its turn like any other news.
func TestUndeclaredInterruptOrProblemWaitsItsTurn(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "Download finished", "notable")))
	r.SpeechStarted(v.ids["Download finished"])
	r.HandleState(undeclared(state("b", 1, true, doc("notice", "", "Act now", "interrupt"))))
	r.HandleState(undeclared(state("c", 1, true, doc("problem", "Disk full", "Free some space", "ambient"))))
	want := []string{"say:Download finished", "say:Act now", "say:Disk full. Free some space"}
	if !reflect.DeepEqual(v.log, want) {
		t.Fatalf("undeclared interrupt and problem queue behind what plays: %v", v.log)
	}
}

// A declared problem cuts in even when its urgency asks for nothing: the
// platform answered interrupt for it, and that answer is the one read.
func TestDeclaredProblemCutsInWhateverItsUrgency(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "Download finished", "notable")))
	r.SpeechStarted(v.ids["Download finished"])
	r.HandleState(state("c", 1, true, doc("problem", "", "Disk full", "ambient")))
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:Disk full"}) {
		t.Fatalf("a declared problem cuts in: %v", got)
	}
}

// A key hold holds everything, an interrupt the platform granted included:
// it is said at the boundary, after the hold, and cuts nothing then.
func TestAHoldKeepsBackADeclaredInterrupt(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "Download finished", "notable")))
	r.SpeechStarted(v.ids["Download finished"])
	r.PipelineStarted("commands", true)
	r.HandleState(state("c", 1, true, doc("problem", "", "Disk full", "interrupt")))
	if got := says(v); !reflect.DeepEqual(got, []string{"say:Download finished"}) {
		t.Fatalf("nothing is said during a hold: %v", got)
	}
	before := len(v.log)
	r.PipelineStopped("commands")
	if got := v.log[before:]; !reflect.DeepEqual(got, []string{"say:Disk full"}) {
		t.Fatalf("held interrupt said at the boundary, without a cut: %v", got)
	}
}

// A channel supersedes its own sentence while that sentence plays, and
// only then: once it has finished, or while another channel's plays, a new
// state queues instead of cutting someone else off.
func TestSameChannelSupersedesOnlyItsOwnPlayingSentence(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "Download finished", "notable")))
	r.HandleState(state("b", 1, true, doc("notice", "", "Back online", "notable")))
	// a is playing, b waits: b's update must not cut a.
	r.HandleState(state("b", 2, true, doc("notice", "", "Offline again", "ambient")))
	if !reflect.DeepEqual(v.log, []string{"say:Download finished", "say:Back online", "say:Offline again"}) {
		t.Fatalf("a waiting channel's update queues: %v", v.log)
	}
	v.finish(r, "Download finished")
	v.finish(r, "Back online")
	// b's own sentence is playing now: its next state cuts it.
	r.HandleState(state("b", 3, true, doc("notice", "", "Online", "ambient")))
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:Online"}) {
		t.Fatalf("same channel supersedes its playing sentence: %v", got)
	}
	v.finish(r, "Online")
	// Nothing plays: a new state on any channel just speaks.
	r.HandleState(state("b", 4, true, doc("notice", "", "Offline", "ambient")))
	if got := v.log[len(v.log)-1]; got != "say:Offline" || v.log[len(v.log)-2] == "stop" {
		t.Fatalf("after the end nothing is cut: %v", v.log)
	}
}

// A notification queued behind another channel's speech is still heard when
// its card times out (its channel is cleared) before its turn comes: the
// clear must not stop the other channel's sentence, and so the queue.
func TestClearOfAWaitingChannelCutsNothing(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("discovery", 1, true, doc("choices", "", "snap left. 6 commands", "ambient", "left", "right")))
	r.HandleState(state("notification", 1, true, doc("notice", "Download finished", "The model is ready", "notable")))
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "notification", PluginID: "_platform", Generation: 2})
	for _, l := range v.log {
		if l == "stop" {
			t.Fatalf("a waiting channel's clear stopped the voice: %v", v.log)
		}
	}
	// Clearing the channel that plays stops it; what waited is said again.
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "discovery", PluginID: "voice", Generation: 2})
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:Download finished. The model is ready"}) {
		t.Fatalf("clear of the playing channel: %v", got)
	}
}

// An utterance whose end was never reported does not hold the queue: the
// next one starting means everything ahead of it is over.
func TestStartedDropsWhatWasAheadOfIt(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "one", "notable")))
	r.HandleState(state("b", 1, true, doc("notice", "", "two", "notable")))
	r.SpeechStarted(v.ids["two"])
	// b plays now, so b's update supersedes it and a is not re-said.
	r.HandleState(state("b", 2, true, doc("notice", "", "three", "notable")))
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:three"}) {
		t.Fatalf("started moved the head: %v", got)
	}
}

// The head is queued but its engine has not made a sound yet: an interrupt
// drops it unheard, so it is said again after the interrupt.
func TestAnInterruptReSaysAnUnheardHead(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("a", 1, true, doc("notice", "", "one", "notable")))
	r.HandleState(state("b", 1, true, doc("notice", "", "two", "notable")))
	r.SpeechStarted(v.ids["one"])
	v.finish(r, "one")
	r.HandleState(state("c", 1, true, doc("problem", "", "Disk full", "interrupt")))
	if got := v.log[len(v.log)-3:]; !reflect.DeepEqual(got, []string{"stop", "say:Disk full", "say:two"}) {
		t.Fatalf("an unheard head is said again: %v", got)
	}
	// Once heard, the cut head is not repeated.
	r.SpeechStarted(v.ids["Disk full"])
	r.HandleState(state("d", 1, true, doc("problem", "", "Mic lost", "interrupt")))
	if got := v.log[len(v.log)-3:]; !reflect.DeepEqual(got, []string{"stop", "say:Mic lost", "say:two"}) {
		t.Fatalf("a heard head is not repeated: %v", got)
	}
}

// Words handed to a voice that is no longer the one speaking (VoiceOver
// turned on, another engine loaded) are beyond a high-priority cut, which
// reaches only the new voice's queue: they are not cut, nor said a second
// time through the new voice. A clear still may not Stop (which silences
// every voice) while another channel is heard on the old one.
func TestAVoiceChangeLeavesTheOldVoicesQueue(t *testing.T) {
	r, v := renderer(t, on)
	r.HandleState(state("x", 1, true, doc("notice", "", "one", "notable")))
	r.HandleState(state("b", 1, true, doc("notice", "", "two", "notable")))
	r.SpeechStarted(v.ids["one"])
	v.route = "kitten"
	r.HandleState(state("a", 2, true, doc("notice", "", "three", "notable")))
	if !reflect.DeepEqual(v.log, []string{"say:one", "say:two", "say:three"}) {
		t.Fatalf("nothing of the old voice is cut or repeated: %v", v.log)
	}
	r.HandleState(state("c", 1, true, doc("mode", "", "quiet mode", "notable")))
	before := len(v.log)
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "a", PluginID: "voice", Generation: 9})
	if len(v.log) != before {
		t.Fatalf("a clear cut the old voice's heard sentence: %v", v.log)
	}
	// The old voice is done: now a's clear may stop the new one, and what
	// waited there is said again.
	v.finish(r, "one")
	v.finish(r, "two")
	r.HandleClear(branchkit.OutputClearedEventParams{Channel: "a", PluginID: "voice", Generation: 10})
	if got := v.log[len(v.log)-2:]; !reflect.DeepEqual(got, []string{"stop", "say:quiet mode"}) {
		t.Fatalf("a clear of the playing channel: %v", got)
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
