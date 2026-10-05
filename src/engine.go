package main

// Which voice speaks: the system's, or a local neural voice this plugin ships
// as speech engine stages (`provides.stages`, one per model, all the same
// `sherpa_tts` binary). The person picks one; it speaks only once its model
// is downloaded and the engine has started, and until then the system voice
// does, so choosing a voice never makes BranchKit go quiet.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/branchkit/plugin-sdk-go"
)

// engineChoice is one voice the settings offer.
type engineChoice struct {
	key      string // the `engine` setting, and the stage name
	label    string
	artifact string // the model to download; empty for system and custom
	note     string
}

// engineChoices, in the order the picker shows them. Kitten is the default:
// eight voices in 27 MB, about 0.2 s to first sound on a short line (measured
// on an M-series Air, 2026-10-05). Kokoro sounds the most natural but took
// over a second to start a sentence there, so it is offered, not chosen.
var engineChoices = []engineChoice{
	{key: "system", label: "System voice", note: "The voice set in your computer's settings."},
	{key: "kitten", label: "Kitten (8 voices)", artifact: "kitten-nano-en-v0_1-fp16", note: "Local, small and quick: 27 MB."},
	{key: "piper_amy", label: "Piper Amy", artifact: "vits-piper-en_US-amy-low", note: "Local, the fastest; one voice: 67 MB."},
	{key: "kokoro", label: "Kokoro (11 voices)", artifact: "kokoro-en-v0_19", note: "Local, the most natural, slower to start: 320 MB."},
	{key: "custom", label: "Your own voice", note: "Any sherpa-onnx voice you add (see below)."},
}

func choiceFor(key string) engineChoice {
	for _, c := range engineChoices {
		if c.key == key {
			return c
		}
	}
	return engineChoices[0]
}

// customVoiceFile is where this plugin tells the `custom` engine which model
// to load: in the plugin's data folder, which the stage shares.
const customVoiceFile = "custom-voice.json"

// customVoicesDir is the folder a person drops sherpa-onnx voices into.
const customVoicesDir = "voices"

// engineState is what the last check of the chosen engine found.
type engineState struct {
	mu sync.Mutex
	// effective is the stage `speech.say` names, or "" for the system voice.
	effective string
	voices    []branchkit.SpeechVoice
	problem   string // why the chosen engine is not the one speaking
}

func (e *engineState) get() (string, []branchkit.SpeechVoice, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.effective, append([]branchkit.SpeechVoice(nil), e.voices...), e.problem
}

func (e *engineState) set(effective string, voices []branchkit.SpeechVoice, problem string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.effective, e.voices, e.problem = effective, voices, problem
	// Every outcome, not only success: "why is it the system voice?" is
	// answered here or nowhere.
	if problem != "" {
		branchkit.Logf("announcements", "voice: %s", problem)
	} else if effective == "" {
		branchkit.Logf("announcements", "voice: the system voice")
	}
}

// stageName is the qualified engine name `speech.say` takes.
func (h *Host) stageName(key string) string {
	return h.plugin.ID() + "." + key
}

// artifactInstalled reports whether a declared model is on disk.
func artifactInstalled(name string) bool {
	root := branchkit.ModelsDir()
	if root == "" || name == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.IsDir()
}

// writeCustomVoice points the custom engine at the person's chosen folder.
func writeCustomVoice(model, family string) error {
	dir := branchkit.PluginDataDir()
	if dir == "" {
		return os.ErrNotExist
	}
	raw, err := json.Marshal(map[string]string{
		"model":  filepath.Join(customVoicesDir, model),
		"family": family,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, customVoiceFile), raw, 0o644)
}

// customVoiceFolders lists what the person has put in the voices folder.
func customVoiceFolders() []string {
	dir := branchkit.PluginDataDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(dir, customVoicesDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name()[0] != '.' {
			out = append(out, e.Name())
		}
	}
	return out
}

// checkEngine starts the chosen engine (if it can) and records which voice
// will actually speak. Run off the event loop: a model takes up to seconds
// to load.
func (h *Host) checkEngine() {
	c := h.config()
	choice := choiceFor(c.Engine)
	if choice.key == "system" {
		h.engine.set("", nil, "")
		return
	}
	if choice.artifact != "" && !artifactInstalled(choice.artifact) {
		h.engine.set("", nil, choice.label+" is not downloaded yet; the system voice speaks meanwhile.")
		return
	}
	if choice.key == "custom" {
		if c.CustomModel == "" {
			h.engine.set("", nil, "Choose your voice's folder below; the system voice speaks meanwhile.")
			return
		}
		if err := writeCustomVoice(c.CustomModel, c.CustomFamily); err != nil {
			h.engine.set("", nil, "Could not save your voice choice: "+err.Error())
			return
		}
		// A different folder or kind: start the engine afresh on it.
		_ = h.plugin.SpeechRestartEngine(branchkit.SpeechRestartEngineRequest{Engine: h.stageName("custom")})
	}
	name := h.stageName(choice.key)
	engines, err := h.plugin.SpeechEngines(branchkit.SpeechEnginesRequest{Engine: &name})
	if err != nil {
		h.engine.set("", nil, choice.label+" could not start: "+err.Error())
		return
	}
	for _, e := range engines {
		if e.Engine != name {
			continue
		}
		if e.Error != nil {
			h.engine.set("", nil, choice.label+" could not start: "+*e.Error)
			return
		}
		h.engine.set(name, e.Voices, "")
		branchkit.Logf("announcements", "voice: speaking with %s (%d voices)", name, len(e.Voices))
		return
	}
	h.engine.set("", nil, choice.label+" is not available on this computer.")
}
