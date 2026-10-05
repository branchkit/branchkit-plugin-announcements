// Announcements: a renderer of BranchKit's semantic output channel (spoken today).
//
// Every plugin with something the person needs to know says so with
// `output.state` — words, no markup — and the platform emits each one as
// `_platform.output.state`. This plugin listens and decides what, if
// anything, to say (renderer.go), then says it through the platform's
// speech primitive. The split is the platform's usual one: the shell makes
// the sound and reports its span so the microphone's echo is dropped; the
// opinion about what is worth hearing lives here, where a person can turn
// it, and where any third party could have written it.
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/branchkit/plugin-sdk-go"
)

// SpeechConfig mirrors the plugin's settings collection (plugin.json).
type SpeechConfig struct {
	Speak string `json:"speak"`
	// Enabled is the bool `speak` replaced; read only to carry a stored
	// choice over (true -> "yes", anything else -> "no").
	Enabled       *bool  `json:"enabled,omitempty"`
	SpeakOutcomes bool   `json:"speak_outcomes"`
	ChoicesLimit  int    `json:"choices_limit"`
	Sight         string `json:"sight"`
	Hearing       string `json:"hearing"`
	// Which voice speaks: "system" or one of this plugin's engine stages
	// (engine.go).
	Engine string `json:"engine"`
	// One of the engine's voices; empty is its default.
	Voice string `json:"voice"`
	// Pace: 1.0 is the voice's normal one.
	Rate float64 `json:"rate"`
	// The person's own voice: a folder under the data folder's voices/, and
	// which kind of sherpa-onnx model it is (kitten, kokoro, vits).
	CustomModel  string `json:"custom_model"`
	CustomFamily string `json:"custom_family"`
}

// accessibilityCollection is the platform fact the shell keeps true:
// `{voice_over}`. Followed by default for "can you see the screen".
const accessibilityCollection = "_platform.accessibility"

type accessibilityRecord struct {
	VoiceOver bool `json:"voice_over"`
}

const configCollection = "plugin.announcements.config"

func defaultConfig() SpeechConfig {
	// Speak defaults to "no": spoken announcements and their VoiceOver
	// routing are new and not yet designed or tested for the people they
	// are meant for (2026-09-23), so nobody gets them without asking.
	return SpeechConfig{
		Speak: "no", ChoicesLimit: 6, Sight: "system", Hearing: "yes",
		Engine: "kitten", Rate: 1.0, CustomFamily: "vits",
	}
}

type Host struct {
	plugin *branchkit.Plugin
	cfg    *branchkit.SettingsMirror[SpeechConfig]
	a11y   *branchkit.CollectionMirror
	r      *Renderer
	engine engineState
	// What the engine was last checked against, so a change elsewhere in
	// the settings does not restart a model.
	engineKey string
	engineMu  sync.Mutex
}

// sdkVoice speaks through the platform: `speech.say` / `speech.stop`, or
// `speech.announce` when the policy routes through VoiceOver.
type sdkVoice struct {
	p         *branchkit.Plugin
	voiceOver atomic.Bool
	// with answers which engine, voice and pace to speak with now.
	with func() (engine, voice string, rate float64)
}

func (v *sdkVoice) UseVoiceOver(on bool) { v.voiceOver.Store(on) }

func (v *sdkVoice) Say(text string, high bool) {
	if v.voiceOver.Load() {
		// A high-priority announcement supersedes what VoiceOver is saying;
		// there is no queue to speak of, so priority is moot.
		if err := v.p.SpeechAnnounce(branchkit.SpeechAnnounceRequest{Text: text}); err != nil {
			branchkit.Logf("announcements", "speech.announce failed: %v", err)
		}
		return
	}
	priority := "normal"
	if high {
		priority = "high"
	}
	req := branchkit.SpeechSayRequest{Text: text, Priority: &priority}
	if v.with != nil {
		if engine, voice, rate := v.with(); engine != "" {
			req.Engine = &engine
			if voice != "" {
				req.Voice = &voice
			}
			if rate > 0 && rate != 1 {
				req.Rate = &rate
			}
		}
	}
	if _, err := v.p.SpeechSay(req); err != nil {
		branchkit.Logf("announcements", "speech.say failed: %v", err)
	}
}

func (v *sdkVoice) Stop() {
	if v.voiceOver.Load() {
		return // VoiceOver announcements cannot be cancelled from here
	}
	if err := v.p.SpeechStop(); err != nil {
		branchkit.Logf("announcements", "speech.stop failed: %v", err)
	}
}

func (c SpeechConfig) speak() string {
	switch c.Speak {
	case "system", "yes", "no":
		return c.Speak
	}
	if c.Enabled != nil && *c.Enabled {
		return "yes"
	}
	return "no"
}

func (h *Host) config() SpeechConfig {
	if h.cfg != nil && h.cfg.Ready() {
		return h.cfg.Get()
	}
	return defaultConfig()
}

func (h *Host) voiceOverOn() bool {
	if h.a11y == nil {
		return false
	}
	var raw json.RawMessage
	if err := h.a11y.Decode(&raw); err != nil {
		return false
	}
	return decodeVoiceOver(raw)
}

// decodeVoiceOver reads the `_platform.accessibility` snapshot. The collection
// is keyed by field (`id`), so a read returns an ARRAY of records, not the one
// object it holds — decoding straight into a struct failed silently and left
// "follow VoiceOver" permanently off. A lone object is accepted too, so a
// change to a singleton cannot silently mute this again.
func decodeVoiceOver(raw json.RawMessage) bool {
	var recs []accessibilityRecord
	if err := json.Unmarshal(raw, &recs); err == nil {
		for _, r := range recs {
			if r.VoiceOver {
				return true
			}
		}
		return false
	}
	var rec accessibilityRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return false
	}
	return rec.VoiceOver
}

// recheckEngine runs checkEngine when what decides the engine changed.
func (h *Host) recheckEngine(force bool) {
	c := h.config()
	key := c.Engine + "|" + c.CustomModel + "|" + c.CustomFamily
	h.engineMu.Lock()
	changed := key != h.engineKey
	h.engineKey = key
	h.engineMu.Unlock()
	if changed || force {
		go h.checkEngine()
	}
}

func (h *Host) applyPolicy() {
	h.recheckEngine(false)
	c := h.config()
	pol := DerivePolicy(Profile{
		Speak: c.speak(), SpeakOutcomes: c.SpeakOutcomes, ChoicesLimit: c.ChoicesLimit,
		Sight: c.Sight, Hearing: c.Hearing,
	}, h.voiceOverOn())
	h.r.Apply(pol)
}

// decodeEvent reads an event's params into v. The bus delivers the event's
// params directly; an envelope with a `data` object is accepted too, so a
// transport change cannot silently mute the renderer.
func decodeEvent(raw json.RawMessage, v any) error {
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && len(probe.Data) > 0 && probe.Data[0] == '{' {
		return json.Unmarshal(probe.Data, v)
	}
	return json.Unmarshal(raw, v)
}

func main() {
	p := branchkit.NewPlugin()
	h := &Host{plugin: p}
	h.r = NewRenderer(&sdkVoice{p: p, with: func() (string, string, float64) {
		engine, _, _ := h.engine.get()
		c := h.config()
		return engine, c.Voice, c.Rate
	}})
	h.cfg = branchkit.Settings[SpeechConfig](p, configCollection)
	h.cfg.OnChange(func(SpeechConfig) { h.applyPolicy() })
	// The one platform fact the profile follows by default.
	h.a11y = p.MirrorCollection(accessibilityCollection)
	h.a11y.OnChange(func() { h.applyPolicy() })
	// A finished download may be the model the chosen voice was waiting for.
	downloads := p.MirrorCollection("_platform.model_download")
	downloads.OnChange(func() { h.recheckEngine(true) })
	p.OnReady(func() { h.applyPolicy() })

	p.On("_platform.output.state", func(raw json.RawMessage) {
		var m branchkit.OutputStateEventParams
		if err := decodeEvent(raw, &m); err != nil {
			branchkit.Logf("announcements", "output.state: undecodable event: %v", err)
			return
		}
		h.r.HandleState(m)
	})
	p.On("_platform.output.cleared", func(raw json.RawMessage) {
		var m branchkit.OutputClearedEventParams
		if err := decodeEvent(raw, &m); err != nil {
			return
		}
		h.r.HandleClear(m)
	})
	p.On("_platform.pipeline.started", func(raw json.RawMessage) {
		var m branchkit.PipelineStartedEventParams
		if err := decodeEvent(raw, &m); err != nil {
			return
		}
		h.r.PipelineStarted(m.Pipeline, m.Ephemeral != nil && *m.Ephemeral)
	})
	p.On("_platform.pipeline.stopped", func(raw json.RawMessage) {
		var m branchkit.PipelineStoppedEventParams
		if err := decodeEvent(raw, &m); err != nil {
			return
		}
		h.r.PipelineStopped(m.Pipeline)
	})

	p.SettingsTab("announcements", h.renderSettings)
	branchkit.HandleCommand(p, "set_speak", func(req *setChoiceRequest) error {
		switch req.Value {
		case "system", "yes", "no":
			return h.cfg.SetUser("speak", req.Value)
		}
		return fmt.Errorf("speak must be system, yes or no, got %q", req.Value)
	})
	branchkit.HandleCommand(p, "set_speak_outcomes", func(req *setBoolRequest) error {
		return h.cfg.SetUser("speak_outcomes", req.Enabled)
	})
	branchkit.HandleCommand(p, "set_sight", func(req *setChoiceRequest) error {
		switch req.Value {
		case "system", "yes", "no":
			return h.cfg.SetUser("sight", req.Value)
		}
		return fmt.Errorf("sight must be system, yes or no, got %q", req.Value)
	})
	branchkit.HandleCommand(p, "set_hearing", func(req *setChoiceRequest) error {
		switch req.Value {
		case "yes", "no":
			return h.cfg.SetUser("hearing", req.Value)
		}
		return fmt.Errorf("hearing must be yes or no, got %q", req.Value)
	})
	branchkit.HandleCommand(p, "set_engine", func(req *setChoiceRequest) error {
		if choiceFor(req.Value).key != req.Value {
			return fmt.Errorf("unknown voice %q", req.Value)
		}
		if err := h.cfg.SetUser("engine", req.Value); err != nil {
			return err
		}
		// A different engine has different speakers.
		return h.cfg.SetUser("voice", "")
	})
	branchkit.HandleCommand(p, "set_voice", func(req *setChoiceRequest) error {
		return h.cfg.SetUser("voice", req.Value)
	})
	branchkit.HandleCommand(p, "set_rate", func(req *setLimitRequest) error {
		r, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(req.Limit)), 64)
		if err != nil || r < 0.5 || r > 3 {
			return fmt.Errorf("rate must be 0.5..3, got %v", req.Limit)
		}
		return h.cfg.SetUser("rate", r)
	})
	branchkit.HandleCommand(p, "set_custom_model", func(req *setChoiceRequest) error {
		return h.cfg.SetUser("custom_model", req.Value)
	})
	branchkit.HandleCommand(p, "set_custom_family", func(req *setChoiceRequest) error {
		switch req.Value {
		case "kitten", "kokoro", "vits":
			return h.cfg.SetUser("custom_family", req.Value)
		}
		return fmt.Errorf("kind must be kitten, kokoro or vits, got %q", req.Value)
	})
	branchkit.HandleCommand(p, "download_engine", func(req *setChoiceRequest) error {
		c := choiceFor(req.Value)
		if c.artifact == "" {
			return fmt.Errorf("%q has nothing to download", req.Value)
		}
		return p.ControlSignal(branchkit.ControlSignalRequest{Signal: "download_model:" + p.ID() + "/" + c.artifact})
	})
	branchkit.HandleCommand(p, "set_choices_limit", func(req *setLimitRequest) error {
		n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(req.Limit)))
		if err != nil || n < 1 || n > 50 {
			return fmt.Errorf("choices_limit must be 1..50, got %v", req.Limit)
		}
		return h.cfg.SetUser("choices_limit", n)
	})

	p.Run()
}

type setBoolRequest struct {
	Enabled bool `json:"enabled"`
}

// Datastar posts `el.value` as a string; a bare number is accepted too.
type setLimitRequest struct {
	Limit any `json:"limit"`
}

type setChoiceRequest struct {
	Value string `json:"value"`
}

func (h *Host) renderSettings(_ *branchkit.RenderSettingsRequest) (string, error) {
	c := h.config()
	checked := func(b bool) string {
		if b {
			return " checked"
		}
		return ""
	}
	var limits strings.Builder
	for _, n := range []int{3, 6, 12, 24} {
		sel := ""
		if n == c.ChoicesLimit {
			sel = " selected"
		}
		fmt.Fprintf(&limits, `<option value="%d"%s>%d</option>`, n, sel, n)
	}
	option := func(value, label, current string) string {
		sel := ""
		if value == current {
			sel = " selected"
		}
		return fmt.Sprintf(`<option value="%s"%s>%s</option>`, value, sel, label)
	}
	sight := option("system", "Follow VoiceOver", c.Sight) + option("yes", "Yes", c.Sight) + option("no", "No", c.Sight)
	hearing := option("yes", "Yes", c.Hearing) + option("no", "No", c.Hearing)
	speak := option("system", "When I cannot see the screen", c.speak()) + option("yes", "Always", c.speak()) + option("no", "Never", c.speak())
	vo := "off"
	if h.voiceOverOn() {
		vo = "on"
	}
	return fmt.Sprintf(`
<div class="announcements-tab">
  <!-- No title: the platform header above this tab already names the plugin. -->
  <p class="note">What BranchKit tells you: what went wrong, the mode you are in and the choices it offers,
  and — if you cannot see it happen — what just happened.</p>
  <h3>You</h3>
  <p class="note">Configure the person, not the sounds. What you can perceive decides what is worth saying.</p>
  <div class="row">
    <label>Can you see the screen?</label>
    <select data-on:change="$value = el.value; @post('%s')">%s</select>
    <span class="hint">"Follow VoiceOver" means: when VoiceOver is on, BranchKit assumes you are not reading the screen by eye and speaks through VoiceOver. VoiceOver is %s right now.</span>
  </div>
  <div class="row">
    <label>Can you hear?</label>
    <select data-on:change="$value = el.value; @post('%s')">%s</select>
    <span class="hint">"No" keeps everything on the screen; nothing is spoken.</span>
  </div>
  <h3>Spoken</h3>
  <p class="note">Read aloud through the system voice. Never while you hold a key — the microphone hears the
  speaker — and while continuous listening is on, what the microphone hears back is dropped from recognition.</p>
  <div class="row">
    <label>Speak announcements</label>
    <select data-on:change="$value = el.value; @post('%s')">%s</select>
    <span class="hint">"When I cannot see the screen" follows your answer above — including VoiceOver, when that is what it follows.</span>
  </div>
  <div class="row">
    <bk-toggle%s title="Say what happened" data-on:change="$enabled = el.checked; @post('%s')"></bk-toggle>
    <label>Say what happened</label>
    <span class="hint">"snap right", after it happened. On by itself when you cannot see the screen; this switch turns it on for everyone else.</span>
  </div>
  %s
  <div class="row">
    <label>Choices read aloud</label>
    <select data-on:change="$limit = el.value; @post('%s')">%s</select>
    <span class="hint">A mode's choices: its phrase, then this many item phrases, then "and N more".</span>
  </div>
</div>`,
		html.EscapeString(branchkit.MethodURL("set_sight")), sight, vo,
		html.EscapeString(branchkit.MethodURL("set_hearing")), hearing,
		html.EscapeString(branchkit.MethodURL("set_speak")), speak,
		checked(c.SpeakOutcomes), html.EscapeString(branchkit.MethodURL("set_speak_outcomes")),
		h.renderVoice(c),
		html.EscapeString(branchkit.MethodURL("set_choices_limit")), limits.String(),
	), nil
}

// renderVoice is the Voice part of the tab: which voice speaks, its speaker
// and pace, a download for a voice not on disk yet, and the person's own.
func (h *Host) renderVoice(c SpeechConfig) string {
	esc := html.EscapeString
	option := func(value, label, current string) string {
		sel := ""
		if value == current {
			sel = " selected"
		}
		return fmt.Sprintf(`<option value="%s"%s>%s</option>`, esc(value), sel, esc(label))
	}
	var engines strings.Builder
	for _, e := range engineChoices {
		engines.WriteString(option(e.key, e.label, c.Engine))
	}
	choice := choiceFor(c.Engine)
	effective, voices, problem := h.engine.get()

	var b strings.Builder
	b.WriteString(`<h3>Voice</h3>`)
	fmt.Fprintf(&b, `<div class="row"><label>Voice</label><select data-on:change="$value = el.value; @post('%s')">%s</select><span class="hint">%s</span></div>`,
		esc(branchkit.MethodURL("set_engine")), engines.String(), esc(choice.note))

	if choice.artifact != "" && !artifactInstalled(choice.artifact) {
		fmt.Fprintf(&b, `<div class="row"><button data-on:click="$value = '%s'; @post('%s')">Download %s</button><span class="hint">Downloaded once, then it runs on this computer with no network.</span></div>`,
			esc(choice.key), esc(branchkit.MethodURL("download_engine")), esc(choice.label))
	}
	if problem != "" {
		fmt.Fprintf(&b, `<p class="note">%s</p>`, esc(problem))
	}
	if effective != "" && len(voices) > 1 {
		var opts strings.Builder
		opts.WriteString(option("", "Default ("+voices[0].Name+")", c.Voice))
		for _, v := range voices {
			opts.WriteString(option(v.ID, v.Name, c.Voice))
		}
		fmt.Fprintf(&b, `<div class="row"><label>Speaker</label><select data-on:change="$value = el.value; @post('%s')">%s</select></div>`,
			esc(branchkit.MethodURL("set_voice")), opts.String())
	}
	if effective != "" {
		var rates strings.Builder
		for _, r := range []float64{0.8, 1.0, 1.25, 1.5, 2.0, 2.5} {
			sel := ""
			if r == c.Rate {
				sel = " selected"
			}
			fmt.Fprintf(&rates, `<option value="%g"%s>%g×</option>`, r, sel, r)
		}
		fmt.Fprintf(&b, `<div class="row"><label>Speed</label><select data-on:change="$limit = el.value; @post('%s')">%s</select><span class="hint">Experienced listeners often prefer 2× or faster.</span></div>`,
			esc(branchkit.MethodURL("set_rate")), rates.String())
	}

	if choice.key == "custom" {
		folders := customVoiceFolders()
		voicesPath := filepath.Join(branchkit.PluginDataDir(), customVoicesDir)
		fmt.Fprintf(&b, `<p class="note">Your own voice: put a sherpa-onnx text-to-speech model folder (for example one from github.com/k2-fsa/sherpa-onnx/releases/tag/tts-models, unpacked) in <code>%s</code>, then choose it here and say which kind it is.</p>`, esc(voicesPath))
		var opts strings.Builder
		opts.WriteString(option("", "Choose a folder", c.CustomModel))
		for _, f := range folders {
			opts.WriteString(option(f, f, c.CustomModel))
		}
		fmt.Fprintf(&b, `<div class="row"><label>Folder</label><select data-on:change="$value = el.value; @post('%s')">%s</select></div>`,
			esc(branchkit.MethodURL("set_custom_model")), opts.String())
		kinds := option("vits", "Piper / VITS", c.CustomFamily) + option("kitten", "Kitten", c.CustomFamily) + option("kokoro", "Kokoro", c.CustomFamily)
		fmt.Fprintf(&b, `<div class="row"><label>Kind</label><select data-on:change="$value = el.value; @post('%s')">%s</select></div>`,
			esc(branchkit.MethodURL("set_custom_family")), kinds)
	}
	return b.String()
}
