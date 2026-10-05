# Announcements

Tells you, out loud, what went wrong, the mode you are in and the choices it
offers, and, if you cannot see the screen, what just happened. A plugin for
[BranchKit](https://github.com/branchkit), an accessibility plugin platform
for the desktop. MIT licensed.

BranchKit is pre-launch: the app is not publicly released yet. Spoken
announcements are new and not yet tested with the people they are meant for,
so they are **off by default**: nothing is spoken until you turn it on.

## How it works

Every plugin with something the person needs to know says so with
`output.state`: a short document in plain language, with no markup. The
platform emits each one on the bus as `_platform.output.state`. This plugin
listens, decides what (if anything) is worth saying, and says it through the
platform's speech operation (`speech.say`).

It holds the policy; the platform holds the sound. The platform speaks the
words and records when it did, so whatever the microphone hears back is
dropped from recognition without this plugin knowing. And because it is a
plugin on a public event, it is one renderer among possible others: a
producer never decides whether its feedback is spoken, and anyone could write
a different renderer.

## What it says

With speaking on:

| Document kind | Spoken |
|---|---|
| `problem` | Always: what went wrong, then the fix |
| `mode` | When the mode changes |
| `choices` | The choices' phrase, then the first N item phrases, then "and N more" |
| `outcome` ("snap right") | Only when **Say what happened** is on, or when you cannot see the screen |
| `notice` (a notification) | Always: its title, then its message |
| `progress` | Never |
| anything else | Its phrase |

A document whose meaning did not change since the last one on its channel is
not spoken again. While a key is held (a command or dictation hold), nothing
is spoken: a document that arrives then is held, newest per channel, and
spoken when the key is released, and a hold that starts mid-sentence cuts the
sentence off. During continuous listening it does speak, since its echo is
dropped from recognition.

## Settings

The **Announcements** tab asks about the person rather than the sounds:

| Setting | Values | Default |
|---|---|---|
| Can you see the screen? | Follow VoiceOver, Yes, No | Follow VoiceOver |
| Can you hear? | Yes, No | Yes |
| Speak announcements | When I cannot see the screen, Always, Never | Never |
| Say what happened | on, off | off |
| Choices read aloud | 3, 6, 12, 24 | 6 |
| Voice | System voice, Kitten, Piper Amy, Kokoro, Your own voice | Kitten |
| Speaker | the chosen voice's speakers | its default |
| Speed | 0.8× to 2.5× | 1× |

Answering "No" to hearing means nothing is spoken, whatever else is set. Not
seeing the screen turns on "what happened" by itself. While VoiceOver is
running, announcements go through VoiceOver (`speech.announce`), in the voice
and rate the person already chose, unless they answered Yes to seeing the
screen.

## Voices

Besides the system voice, this plugin ships a local neural speech engine,
`sherpa_tts` (`stages/sherpa-tts`), declared as one speech engine stage per
voice model. Each runs on this computer with no network once its model is
downloaded:

| Voice | Download | Speakers | First sound* | Notes |
|---|---|---|---|---|
| Kitten (default) | 27 MB | 8 | ~0.2 s | small and quick |
| Piper Amy | 67 MB | 1 | <0.1 s | the fastest |
| Kokoro | 320 MB | 11, American and British | ~1.2 s | the most natural, slower to start |
| Your own | — | — | — | any sherpa-onnx text-to-speech model you add |

*On a short line, measured on an Apple-silicon MacBook Air.

Until the chosen voice is downloaded and started, the system voice speaks, so
picking a voice never makes the computer go quiet. **Your own voice**: unpack
a sherpa-onnx text-to-speech model folder (for example from the
[sherpa-onnx model releases](https://github.com/k2-fsa/sherpa-onnx/releases/tag/tts-models))
into the `voices` folder the settings tab names, choose it, and say which kind
of model it is (Piper/VITS, Kitten or Kokoro).

The engine speaks through the platform's speech engine contract: each request
is synthesized a sentence at a time, each sentence is played as soon as it
exists, and a cancel (the person starting to talk) stops it at once. The
platform plays the audio and drops BranchKit's own voice from recognition.

## Permissions

| Privilege | Why |
|---|---|
| `speech` | Speak through the system voice and post VoiceOver announcements |
| `events.output` | Receive what other plugins show and say (`_platform.output.state`) |

It also reads `_platform.accessibility` (whether VoiceOver is running). No
network: the manifest declares no hosts, so the sandbox gives it none.

## Platform support

- **macOS**: the system voice, and VoiceOver when it is running.
- **Linux**: the system voice through speech-dispatcher, which must be
  installed; without it, speaking is refused with a message saying so.
- **Windows**: the system voice through SAPI, using the voice set in
  Settings → Speech.

On Linux and Windows the platform does not yet report a running screen
reader, so "Follow VoiceOver" has nothing to follow there; answer Yes or No
instead.

## Reading this as an example

The whole policy is one pure function, `Utterance` in `src/renderer.go`, and
the person's answers become a policy in another, `DerivePolicy`; both are
unit-tested without a platform. `src/main.go` wires them to the bus with
typed event params (`branchkit.OutputStateEventParams`), mirrors its settings
with `branchkit.Settings[...]`, and speaks through the SDK's typed wrappers
(`plugin.SpeechSay(...)`).

## Build

The plugin: Go 1.25, [plugin-sdk-go](https://github.com/branchkit/plugin-sdk-go).

```bash
cd src && go build -o ../announcements-plugin . && go test ./...
```

The speech engine: Rust, the
[stage SDK](https://github.com/branchkit/stage-sdk), and stock
[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx), whose prebuilt static
libraries the build downloads:

```bash
cargo build --release --manifest-path stages/sherpa-tts/Cargo.toml
cp stages/sherpa-tts/target/release/sherpa_tts .
```

`scripts/build.sh <goos> <goarch> <rust-target>` builds both for one target,
the way CI and the release do; CI then packages the result and checks the
archive (`scripts/check-archive.sh`) on every push.

## License

The source is MIT; see [LICENSE](LICENSE). The built speech engine binary is
distributed under the GPL-3.0, because it statically links
[espeak-ng](https://github.com/espeak-ng/espeak-ng); see [NOTICE](NOTICE).
