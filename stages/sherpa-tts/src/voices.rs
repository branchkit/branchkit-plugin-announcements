//! The voices a model offers, as the person chooses them.
//!
//! sherpa-onnx numbers a multi-speaker model's voices (`sid` 0..n) and keeps
//! no names, so the names come from the upstream releases, by model. A model
//! not listed here, or one whose speaker count disagrees with its table,
//! gets plain numbered voices rather than wrong names.

use branchkit_stage_sdk::events::VoiceInfo;

pub struct Voice {
    pub sid: i32,
    pub info: VoiceInfo,
}

/// Kokoro v0.19 (English): `af`/`am` American female/male, `bf`/`bm`
/// British, in the model's speaker order.
const KOKORO_EN_V0_19: &[(&str, &str, &str)] = &[
    ("af", "American, female", "en-US"),
    ("af_bella", "Bella (American)", "en-US"),
    ("af_nicole", "Nicole (American)", "en-US"),
    ("af_sarah", "Sarah (American)", "en-US"),
    ("af_sky", "Sky (American)", "en-US"),
    ("am_adam", "Adam (American)", "en-US"),
    ("am_michael", "Michael (American)", "en-US"),
    ("bf_emma", "Emma (British)", "en-GB"),
    ("bf_isabella", "Isabella (British)", "en-GB"),
    ("bm_george", "George (British)", "en-GB"),
    ("bm_lewis", "Lewis (British)", "en-GB"),
];

/// Kitten nano v0.1: the upstream `expr-voice-<n>-<m|f>` speakers in order.
const KITTEN_NANO_V0_1: &[(&str, &str, &str)] = &[
    ("expr-voice-2-m", "Voice 2, male", "en-US"),
    ("expr-voice-2-f", "Voice 2, female", "en-US"),
    ("expr-voice-3-m", "Voice 3, male", "en-US"),
    ("expr-voice-3-f", "Voice 3, female", "en-US"),
    ("expr-voice-4-m", "Voice 4, male", "en-US"),
    ("expr-voice-4-f", "Voice 4, female", "en-US"),
    ("expr-voice-5-m", "Voice 5, male", "en-US"),
    ("expr-voice-5-f", "Voice 5, female", "en-US"),
];

fn table(
    family: &str,
    model: &str,
) -> Option<&'static [(&'static str, &'static str, &'static str)]> {
    let m = model.to_ascii_lowercase();
    match family {
        "kokoro" if m.contains("kokoro-en-v0_19") => Some(KOKORO_EN_V0_19),
        "kitten" if m.contains("kitten-nano-en-v0_1") => Some(KITTEN_NANO_V0_1),
        _ => None,
    }
}

/// The voices `model` offers, `speakers` of them by the model's own count.
pub fn for_model(family: &str, model: &str, speakers: usize) -> Vec<Voice> {
    match table(family, model) {
        Some(t) if t.len() == speakers => t
            .iter()
            .enumerate()
            .map(|(i, (id, name, locale))| Voice {
                sid: i as i32,
                info: VoiceInfo::new(*id, *name, *locale),
            })
            .collect(),
        _ => (0..speakers)
            .map(|i| Voice {
                sid: i as i32,
                info: VoiceInfo::new(format!("{i}"), format!("Voice {}", i + 1), "en"),
            })
            .collect(),
    }
}

/// The speaker for a request: the named voice, else the first voice in the
/// requested language, else the default (the first).
pub fn sid_for(voices: &[Voice], voice: Option<&str>, locale: Option<&str>) -> i32 {
    if let Some(v) = voice.and_then(|id| voices.iter().find(|v| v.info.id == id)) {
        return v.sid;
    }
    if let Some(loc) = locale {
        let loc = loc.to_ascii_lowercase();
        if let Some(v) = voices
            .iter()
            .find(|v| v.info.locale.to_ascii_lowercase() == loc)
        {
            return v.sid;
        }
    }
    voices.first().map(|v| v.sid).unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_known_model_gets_its_names_and_an_unknown_one_numbers() {
        let v = for_model("kokoro", "kokoro-en-v0_19", 11);
        assert_eq!(v[9].info.id, "bm_george");
        let v = for_model("kokoro", "kokoro-en-v0_19", 3);
        assert_eq!(
            v[2].info.id, "2",
            "a disagreeing count gets numbers, not wrong names"
        );
        assert_eq!(for_model("vits", "vits-piper-en_US-amy-low", 1).len(), 1);
    }

    #[test]
    fn a_named_voice_wins_then_the_language_then_the_default() {
        let v = for_model("kokoro", "kokoro-en-v0_19", 11);
        assert_eq!(sid_for(&v, Some("bm_lewis"), None), 10);
        assert_eq!(sid_for(&v, Some("nobody"), Some("en-GB")), 7);
        assert_eq!(sid_for(&v, None, None), 0);
    }
}
