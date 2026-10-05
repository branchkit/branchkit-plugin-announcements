package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/branchkit/plugin-sdk-go"
)

// Voice is what the renderer speaks through. The real one calls the
// platform's `speech.say` / `speech.stop` primitives (or `speech.announce`
// when the policy routes through VoiceOver); tests use a fake. Say with
// high priority cuts off whatever is playing; without it, queues.
type Voice interface {
	Say(text string, high bool)
	Stop()
	// UseVoiceOver switches Say between the system voice and VoiceOver.
	UseVoiceOver(on bool)
}

// Policy is what the renderer acts on. It is DERIVED from the person's
// answers (Profile) and one platform fact, never edited directly: see
// DerivePolicy.
type Policy struct {
	Enabled       bool
	SpeakOutcomes bool
	ChoicesLimit  int
	// UseVoiceOver routes utterances through VoiceOver announcements
	// (`speech.announce`) instead of the system voice, so a VoiceOver user
	// hears BranchKit in the voice and rate they already chose.
	UseVoiceOver bool
}

// Profile is what the person told us (the settings tab), plus the switch.
type Profile struct {
	// Speak: "system" (speak when they cannot see the screen — said so, or
	// following VoiceOver and it is on), "yes", or "no". A bool could not
	// say "follow", so a VoiceOver user had to find a switch in a visual
	// Settings page before BranchKit would say anything to them.
	Speak         string
	SpeakOutcomes bool // "Say what happened", an override; the profile can turn it on by itself
	ChoicesLimit  int
	// Sight: "system" (follow VoiceOver — on means the screen is not being
	// read by eye), "yes", or "no".
	Sight string
	// Hearing: "yes" or "no". No hearing means nothing is spoken, whatever
	// else is set; the visual forms of feedback are the HUD's today.
	Hearing string
}

// DerivePolicy: configure the person, not the sounds. What varies is what
// they can perceive; everything else follows.
//   - cannot hear → silence (the switch is inert)
//   - cannot see the screen (said so, or following VoiceOver and it is on)
//     → what happened is spoken too, because they cannot see it happen;
//     with VoiceOver on, through VoiceOver
//   - can see → problems, modes and choices, not their own command read back
func DerivePolicy(pr Profile, voiceOverOn bool) Policy {
	cannotSee := pr.Sight == "no" || (pr.Sight != "yes" && voiceOverOn)
	return Policy{
		Enabled:       (pr.Speak == "yes" || (pr.Speak != "no" && cannotSee)) && pr.Hearing != "no",
		SpeakOutcomes: pr.SpeakOutcomes || cannotSee,
		ChoicesLimit:  pr.ChoicesLimit,
		UseVoiceOver:  voiceOverOn && pr.Sight != "yes",
	}
}

type heldDoc struct {
	generation int
	urgency    string
	text       string
}

// Renderer turns output.state documents into utterances.
//
// Two rules it never breaks: it is SILENT while a person holds a key (an
// ephemeral pipeline: commands or dictation) — a document arriving then is
// held, newest per channel, and spoken at the boundary, and a hold starting
// mid-sentence cuts the sentence; and a stale generation never speaks over
// a newer one. During continuous listening (a persistent pipeline) it
// speaks: the platform drops its echo from recognition by onset.
type Renderer struct {
	mu              sync.Mutex
	voice           Voice
	policy          Policy
	listening       map[string]bool // ephemeral pipelines running
	newest          map[string]int  // newest generation seen per channel
	held            map[string]heldDoc
	speakingChannel string
}

func NewRenderer(v Voice) *Renderer {
	return &Renderer{
		voice:     v,
		listening: map[string]bool{},
		newest:    map[string]int{},
		held:      map[string]heldDoc{},
	}
}

func (r *Renderer) Apply(p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	was := r.policy.Enabled
	r.policy = p
	r.voice.UseVoiceOver(p.UseVoiceOver)
	if was && !p.Enabled {
		r.stopAllLocked(false)
	}
}

func (r *Renderer) PipelineStarted(name string, ephemeral bool) {
	if !ephemeral {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listening[name] = true
	// The person is about to talk; whatever we were saying is in their way
	// and, worse, in the recognizer's input.
	r.stopAllLocked(true)
}

func (r *Renderer) PipelineStopped(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.listening, name)
	if len(r.listening) > 0 {
		return
	}
	r.flushHeldLocked()
}

func (r *Renderer) HandleState(msg branchkit.OutputStateEventParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seen, ok := r.newest[msg.Channel]; ok && seen > msg.Generation {
		return
	}
	r.newest[msg.Channel] = msg.Generation
	if !r.policy.Enabled {
		return
	}
	text, ok := Utterance(msg, r.policy)
	if !ok {
		return
	}
	if len(r.listening) > 0 {
		r.held[msg.Channel] = heldDoc{msg.Generation, msg.State.Urgency, text}
		return
	}
	r.speakLocked(msg.Channel, msg.State.Urgency, text)
}

func (r *Renderer) HandleClear(msg branchkit.OutputClearedEventParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seen, ok := r.newest[msg.Channel]; ok && seen > msg.Generation {
		return
	}
	r.newest[msg.Channel] = msg.Generation
	delete(r.held, msg.Channel)
	if r.speakingChannel == msg.Channel {
		r.voice.Stop()
		r.speakingChannel = ""
	}
}

// Utterance is the policy: the words for one document, or false when this
// kind says nothing (or nothing new). Pure.
func Utterance(msg branchkit.OutputStateEventParams, p Policy) (string, bool) {
	// A push whose meaning did not change is a moving clock, not news.
	if !msg.MeaningChanged {
		return "", false
	}
	doc := msg.State
	phrase := strings.TrimSpace(doc.Phrase)
	title := strings.TrimSpace(doc.Title)
	switch doc.Kind {
	case "progress":
		return "", false
	case "mode":
		return firstNonEmpty(phrase, title)
	case "choices":
		items := itemPhrases(doc)
		limit := p.ChoicesLimit
		if limit < 0 {
			limit = 0
		}
		var parts []string
		if lead, ok := firstNonEmpty(phrase, title); ok {
			parts = append(parts, lead)
		} else if len(items) > 0 {
			noun := "choices"
			if len(items) == 1 {
				noun = "choice"
			}
			parts = append(parts, fmt.Sprintf("%d %s", len(items), noun))
		}
		if len(items) > 0 && limit > 0 {
			n := limit
			if n > len(items) {
				n = len(items)
			}
			list := strings.Join(items[:n], ", ")
			if more := len(items) - n; more > 0 {
				list += fmt.Sprintf(", and %d more", more)
			}
			parts = append(parts, list)
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, ": "), true
	case "problem":
		// The title names the problem, the phrase carries the fix; a
		// person who cannot see needs both, in that order.
		if title != "" && phrase != "" && title != phrase {
			return title + ". " + phrase, true
		}
		return firstNonEmpty(phrase, title)
	case "notice":
		// A message addressed to the person — a notification — not their
		// own command read back, so it is spoken whenever speaking is on.
		// The title says who or what, the phrase says the rest.
		if title != "" && phrase != "" && title != phrase {
			return title + ". " + phrase, true
		}
		return firstNonEmpty(phrase, title)
	case "outcome":
		// What happened. A person who can see the window saw it, and what
		// voice reports today is the command's own words — their own voice
		// read back — so this is off unless the person asked for it.
		if !p.SpeakOutcomes {
			return "", false
		}
		return firstNonEmpty(phrase, title)
	default:
		// A kind this renderer does not know: the phrase is the state in
		// words, which is what unknown degrades to.
		return firstNonEmpty(phrase, title)
	}
}

func itemPhrases(doc branchkit.OutputState) []string {
	var out []string
	for _, s := range doc.Sections {
		for _, it := range s.Items {
			if p := strings.TrimSpace(it.Phrase); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func firstNonEmpty(a, b string) (string, bool) {
	if a != "" {
		return a, true
	}
	if b != "" {
		return b, true
	}
	return "", false
}

func (r *Renderer) speakLocked(channel, urgency, text string) {
	// Same channel: the new state supersedes what is playing. Another
	// channel: only something notable or an interrupt cuts in; ambient
	// waits its turn.
	high := r.speakingChannel == channel || (r.speakingChannel != "" && urgency != "ambient")
	branchkit.Logf("announcements", "speak %s (%s%s): %q", channel, urgency, map[bool]string{true: ", cuts in", false: ""}[high], text)
	r.voice.Say(text, high)
	r.speakingChannel = channel
}

// The boundary: the microphone closed, so everything held while it was open
// is spoken now, oldest generation first, one utterance per channel.
func (r *Renderer) flushHeldLocked() {
	type entry struct {
		channel string
		doc     heldDoc
	}
	var pending []entry
	for ch, d := range r.held {
		pending = append(pending, entry{ch, d})
	}
	r.held = map[string]heldDoc{}
	sort.Slice(pending, func(i, j int) bool { return pending[i].doc.generation < pending[j].doc.generation })
	for _, e := range pending {
		branchkit.Logf("announcements", "speak %s (%s, held): %q", e.channel, e.doc.urgency, e.doc.text)
		r.voice.Say(e.doc.text, false)
		r.speakingChannel = e.channel
	}
}

func (r *Renderer) stopAllLocked(keepHeld bool) {
	r.voice.Stop()
	r.speakingChannel = ""
	if !keepHeld {
		r.held = map[string]heldDoc{}
	}
}
