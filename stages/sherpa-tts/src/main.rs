//! `sherpa_tts`: a local neural speech engine (`stage_type` `tts`) on
//! sherpa-onnx — the same library family as the command recognizer, linked
//! the same static way. Each `speak` request is synthesized a sentence at a
//! time and each sentence sent the moment it exists, so the first words play
//! while the rest are made; a cancel stops synthesis at the next sentence.
//!
//! Arguments (from the shipping plugin's manifest):
//!   --model <dir>     the model directory, resolved under the platform's
//!                     artifacts root (`BRANCHKIT_ARTIFACTS_DIR`) unless absolute
//!   --family <name>   kitten | kokoro | vits — which sherpa-onnx model
//!                     config the directory fills
//!   --threads <n>     inference threads (default 2)
//!   --custom <file>   instead of --model/--family: read both from this JSON
//!                     file in the plugin's data folder (`BRANCHKIT_STAGE_DATA`),
//!                     `{"model": "<dir>", "family": "<name>"}`, the dir
//!                     relative to that folder — a voice the person added

mod voices;

use std::ffi::CString;
use std::os::raw::{c_float, c_void};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use branchkit_stage_sdk::events::{AudioFormat, Capability, Speak};
use branchkit_stage_sdk::stage::{self, Flow, Result, SpeakCtx, SpeechEngine};
use branchkit_stage_sdk::stage_log;
use sherpa_onnx_sys as sys;

#[tokio::main(flavor = "current_thread")]
async fn main() {
    stage::run(run()).await
}

fn arg(name: &str) -> Option<String> {
    let args: Vec<String> = std::env::args().collect();
    args.iter()
        .position(|a| a == name)
        .and_then(|i| args.get(i + 1))
        .cloned()
}

/// `<root>/<name>`, or `name` when it is absolute or already exists. The
/// root is the platform's, never re-derived (see `ARTIFACTS_DIR_ENV`).
fn resolve_model_dir(name: &str) -> PathBuf {
    let p = PathBuf::from(name);
    if p.is_absolute() || p.exists() {
        return p;
    }
    if let Some(root) = std::env::var_os(branchkit_stage_sdk::ARTIFACTS_DIR_ENV) {
        return PathBuf::from(root).join(p);
    }
    p
}

/// The loaded synthesizer. The handle is used from one blocking task at a
/// time (the runtime speaks one utterance at a time), never concurrently.
struct Tts(*const sys::SherpaOnnxOfflineTts);
// SAFETY: sherpa-onnx's offline TTS may be driven from any thread; this
// stage never drives it from two at once (see above).
unsafe impl Send for Tts {}
unsafe impl Sync for Tts {}

impl Drop for Tts {
    fn drop(&mut self) {
        // SAFETY: created by SherpaOnnxCreateOfflineTts, destroyed once.
        unsafe { sys::SherpaOnnxDestroyOfflineTts(self.0) }
    }
}

/// Keeps the C strings the config points into alive while it is in use.
struct Strings(Vec<CString>);

impl Strings {
    fn path(&mut self, p: &Path) -> *const std::os::raw::c_char {
        self.text(&p.to_string_lossy())
    }
    fn text(&mut self, s: &str) -> *const std::os::raw::c_char {
        let c = CString::new(s).unwrap_or_default();
        let ptr = c.as_ptr();
        self.0.push(c);
        ptr
    }
}

/// The first of `names` that exists in `dir`.
fn first_file(dir: &Path, names: &[&str]) -> Option<PathBuf> {
    names.iter().map(|n| dir.join(n)).find(|p| p.exists())
}

fn load(dir: &Path, family: &str, threads: i32) -> std::result::Result<Tts, String> {
    let mut s = Strings(Vec::new());
    // SAFETY: an all-zero config is sherpa-onnx's documented "unset"
    // (null pointers, zero numbers); every field we use is set below.
    let mut config: sys::OfflineTtsConfig = unsafe { std::mem::zeroed() };
    let tokens = dir.join("tokens.txt");
    let data = dir.join("espeak-ng-data");
    let model = first_file(dir, &["model.onnx", "model.fp16.onnx", "model.int8.onnx"])
        .or_else(|| {
            // Piper voices name the model after the voice.
            std::fs::read_dir(dir)
                .ok()?
                .flatten()
                .map(|e| e.path())
                .find(|p| p.extension().is_some_and(|x| x == "onnx"))
        })
        .ok_or_else(|| format!("no .onnx model in {}", dir.display()))?;
    match family {
        "kitten" => {
            config.model.kitten.model = s.path(&model);
            config.model.kitten.voices = s.path(&dir.join("voices.bin"));
            config.model.kitten.tokens = s.path(&tokens);
            config.model.kitten.data_dir = s.path(&data);
            config.model.kitten.length_scale = 1.0;
        }
        "kokoro" => {
            config.model.kokoro.model = s.path(&model);
            config.model.kokoro.voices = s.path(&dir.join("voices.bin"));
            config.model.kokoro.tokens = s.path(&tokens);
            config.model.kokoro.data_dir = s.path(&data);
            config.model.kokoro.length_scale = 1.0;
        }
        "vits" => {
            config.model.vits.model = s.path(&model);
            config.model.vits.tokens = s.path(&tokens);
            config.model.vits.data_dir = s.path(&data);
            config.model.vits.noise_scale = 0.667;
            config.model.vits.noise_scale_w = 0.8;
            config.model.vits.length_scale = 1.0;
        }
        other => return Err(format!("unknown --family {other:?} (kitten, kokoro, vits)")),
    }
    config.model.num_threads = threads;
    config.model.provider = s.text("cpu");
    // One sentence per callback: what makes the output stream.
    config.max_num_sentences = 1;
    // SAFETY: `config` and every string it points into live past this call.
    let tts = unsafe { sys::SherpaOnnxCreateOfflineTts(&config) };
    drop(s);
    if tts.is_null() {
        return Err(format!(
            "sherpa-onnx could not load the model in {}",
            dir.display()
        ));
    }
    Ok(Tts(tts))
}

struct Engine {
    tts: Arc<Tts>,
    rate: u32,
    voices: Vec<voices::Voice>,
    /// The synthesis a cancel walked away from, still finishing its
    /// sentence. The next utterance waits for it rather than share the
    /// model; the cancelled one does not, so its close is immediate.
    unfinished: Option<tokio::task::JoinHandle<()>>,
}

/// What the synthesis callback hands the async side.
struct CallbackState {
    tx: tokio::sync::mpsc::UnboundedSender<Vec<f32>>,
    cancel: Arc<AtomicBool>,
}

/// Called by sherpa-onnx with each sentence's samples. Returns 0 to stop.
unsafe extern "C" fn on_samples(
    samples: *const f32,
    n: i32,
    _progress: c_float,
    arg: *mut c_void,
) -> i32 {
    // SAFETY: `arg` is the `CallbackState` the generating call was given,
    // alive for the whole call; `samples` holds `n` floats for this call.
    let st = unsafe { &*(arg as *const CallbackState) };
    if st.cancel.load(Ordering::Relaxed) {
        return 0;
    }
    if n > 0 && !samples.is_null() {
        let chunk = unsafe { std::slice::from_raw_parts(samples, n as usize) }.to_vec();
        if st.tx.send(chunk).is_err() {
            return 0;
        }
    }
    1
}

impl SpeechEngine for Engine {
    async fn speak(&mut self, req: Speak, ctx: &mut SpeakCtx<'_>) -> Result {
        if let Some(prev) = self.unfinished.take() {
            let _ = prev.await;
        }
        let sid = voices::sid_for(&self.voices, req.voice.as_deref(), req.locale.as_deref());
        let speed = req.rate.filter(|r| *r > 0.0).unwrap_or(1.0);
        let cancel = ctx.cancel_flag();
        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel::<Vec<f32>>();
        let tts = self.tts.clone();
        let text = req.text.clone();
        let synth_cancel = cancel.clone();
        let synth = tokio::task::spawn_blocking(move || {
            let text = CString::new(text.replace('\0', " ")).unwrap_or_default();
            let state = CallbackState {
                tx,
                cancel: synth_cancel,
            };
            // SAFETY: zeroed is "unset"; the fields used are set.
            let mut generation: sys::SherpaOnnxGenerationConfig = unsafe { std::mem::zeroed() };
            generation.speed = speed;
            generation.sid = sid;
            generation.silence_scale = 0.2;
            // SAFETY: `tts` outlives the call; `state` lives on this stack
            // frame for the whole call, which is the only place the callback
            // dereferences it.
            let audio = unsafe {
                sys::SherpaOnnxOfflineTtsGenerateWithConfig(
                    tts.0,
                    text.as_ptr(),
                    &generation,
                    Some(on_samples),
                    &state as *const CallbackState as *mut c_void,
                )
            };
            if !audio.is_null() {
                // The whole utterance again, already streamed: freed unread.
                unsafe { sys::SherpaOnnxDestroyOfflineTtsGeneratedAudio(audio) };
            }
        });

        ctx.start(AudioFormat {
            rate: self.rate,
            width: 2,
            channels: 1,
        })
        .await?;
        // A cancel arrives while a sentence is being synthesized, which can
        // take most of a second: watch for it between sentences too, not
        // only when the next one is handed over.
        loop {
            tokio::select! {
                next = rx.recv() => {
                    let Some(samples) = next else { break };
                    let pcm: Vec<u8> = samples
                        .iter()
                        .flat_map(|s| ((s.clamp(-1.0, 1.0) * i16::MAX as f32) as i16).to_le_bytes())
                        .collect();
                    if ctx.audio(&pcm).await? == Flow::Stop {
                        break;
                    }
                }
                _ = tokio::time::sleep(std::time::Duration::from_millis(20)) => {
                    if ctx.cancelled() {
                        break;
                    }
                }
            }
        }
        if ctx.cancelled() {
            // The callback stops synthesis at the end of this sentence; the
            // next utterance waits for that, this one closes now.
            cancel.store(true, Ordering::Relaxed);
            self.unfinished = Some(synth);
        } else {
            let _ = synth.await;
        }
        Ok(())
    }
}

/// The model a `--custom` engine loads: the plugin names it in a file in its
/// data folder, so a voice the person added needs no manifest change.
fn custom_choice(file: &str) -> std::result::Result<(PathBuf, String), String> {
    let data = std::env::var_os(branchkit_stage_sdk::DATA_DIR_ENV)
        .map(PathBuf::from)
        .ok_or("no plugin data folder (BRANCHKIT_STAGE_DATA) to read the custom voice from")?;
    let raw = std::fs::read_to_string(data.join(file))
        .map_err(|e| format!("no custom voice chosen ({file}: {e})"))?;
    let v: serde_json::Value =
        serde_json::from_str(&raw).map_err(|e| format!("{file} is not JSON: {e}"))?;
    let model = v["model"].as_str().ok_or("custom voice: no \"model\"")?;
    let family = v["family"].as_str().ok_or("custom voice: no \"family\"")?;
    let dir = PathBuf::from(model);
    let dir = if dir.is_absolute() {
        dir
    } else {
        data.join(dir)
    };
    Ok((dir, family.to_string()))
}

async fn run() -> Result {
    let threads: i32 = arg("--threads").and_then(|t| t.parse().ok()).unwrap_or(2);
    let (dir, family, model) = match arg("--custom") {
        Some(file) => {
            let (dir, family) = custom_choice(&file)?;
            let name = dir
                .file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_default();
            (dir, family, name)
        }
        None => {
            let model = arg("--model").ok_or("--model <dir> (or --custom <file>) is required")?;
            let family = arg("--family").ok_or("--family kitten|kokoro|vits is required")?;
            (resolve_model_dir(&model), family, model)
        }
    };
    let started = std::time::Instant::now();
    let tts = load(&dir, &family, threads)?;
    // SAFETY: a loaded handle.
    let (rate, speakers) = unsafe {
        (
            sys::SherpaOnnxOfflineTtsSampleRate(tts.0),
            sys::SherpaOnnxOfflineTtsNumSpeakers(tts.0),
        )
    };
    let voices = voices::for_model(&family, &model, speakers.max(1) as usize);
    stage_log::info(&format!(
        "loaded {} ({family}) in {} ms: {rate} Hz, {} voice(s)",
        dir.display(),
        started.elapsed().as_millis(),
        voices.len()
    ));
    let mut cap = Capability::new("tts", "sherpa_tts").persistent();
    for v in &voices {
        cap = cap.voice(v.info.clone());
    }
    let mut engine = Engine {
        tts: Arc::new(tts),
        rate: rate as u32,
        voices,
        unfinished: None,
    };
    stage::serve_speech_engine(cap, &mut engine).await
}
