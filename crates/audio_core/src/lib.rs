//! Local voice timing analysis for KOTAE AI.
//!
//! This crate deliberately produces timing features rather than retaining raw
//! samples. Platform layers own microphone access and feed normalized mono PCM
//! frames into [`VoiceDetector`].

use core::fmt;

const SILENCE_DBFS: f32 = -120.0;

pub const INTERRUPT_FRAME_GUARD_VOICED: u8 = 1 << 0;
pub const INTERRUPT_FRAME_VOICED: u8 = 1 << 1;
pub const INTERRUPT_FRAME_FOREGROUND_VOICED: u8 = 1 << 2;

pub const TEMPORAL_VAD_MAXIMUM_TICK_MS: f64 = 40.0;

#[repr(u8)]
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum IntentionalInterruptPhase {
    #[default]
    Armed = 0,
    Candidate = 1,
    Provisional = 2,
    FastReady = 3,
    Confirmed = 4,
    LegacyOnly = 5,
    Discarded = 6,
}

impl TryFrom<u8> for IntentionalInterruptPhase {
    type Error = DetectorError;

    fn try_from(value: u8) -> Result<Self, Self::Error> {
        match value {
            0 => Ok(Self::Armed),
            1 => Ok(Self::Candidate),
            2 => Ok(Self::Provisional),
            3 => Ok(Self::FastReady),
            4 => Ok(Self::Confirmed),
            5 => Ok(Self::LegacyOnly),
            6 => Ok(Self::Discarded),
            _ => Err(DetectorError::InvalidIntentionalInterruptState),
        }
    }
}

#[repr(u8)]
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum IntentionalInterruptSignal {
    #[default]
    None = 0,
    Voice = 1,
    Foreground = 2,
    ForegroundChange = 3,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct IntentionalInterruptState {
    pub phase: IntentionalInterruptPhase,
    pub score: i16,
    pub foreground_ms: u16,
    pub change_count: u8,
    pub gap_ms: u8,
    pub last_bucket: u8,
    pub last_elapsed_ms: u16,
}

impl Default for IntentionalInterruptState {
    fn default() -> Self {
        Self {
            phase: IntentionalInterruptPhase::Armed,
            score: 0,
            foreground_ms: 0,
            change_count: 0,
            gap_ms: 0,
            last_bucket: 0,
            last_elapsed_ms: 0,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct IntentionalInterruptStep {
    pub state: IntentionalInterruptState,
    pub signal: IntentionalInterruptSignal,
    pub fast_ready: bool,
}

/// Advances a conservative SPRT-shaped intentional-interruption gate.
///
/// The state contains clipped fixed-point evidence and finite counters only.
/// Acoustic levels are consumed for the current transition and are never
/// retained. Any ambiguous voice, lost AEC proof, invalid state, or gap above
/// 80 ms permanently delegates this candidate to the existing legacy gate.
pub fn advance_intentional_interrupt(
    mut state: IntentionalInterruptState,
    frame_flags: u8,
    rms: f64,
    peak: f64,
    credited_ms: u8,
    candidate_elapsed_ms: u16,
    aec_verified: bool,
) -> Result<IntentionalInterruptStep, DetectorError> {
    if !(-24..=32).contains(&state.score)
        || state.gap_ms > 80
        || state.foreground_ms > 2_400
        || state.foreground_ms > candidate_elapsed_ms
        || state.change_count > 8
        || state.last_bucket > 4
        || state.last_elapsed_ms >= candidate_elapsed_ms
        || frame_flags & !0b111 != 0
        || !rms.is_finite()
        || !(0.0..=1.0).contains(&rms)
        || !peak.is_finite()
        || !(0.0..=1.0).contains(&peak)
        || credited_ms == 0
        || credited_ms > 40
        || candidate_elapsed_ms == 0
        || candidate_elapsed_ms > 2_400
    {
        return Err(DetectorError::InvalidIntentionalInterruptState);
    }
    state.last_elapsed_ms = candidate_elapsed_ms;
    if matches!(
        state.phase,
        IntentionalInterruptPhase::LegacyOnly | IntentionalInterruptPhase::Discarded
    ) {
        return Ok(IntentionalInterruptStep {
            state,
            signal: IntentionalInterruptSignal::None,
            fast_ready: false,
        });
    }
    if state.phase == IntentionalInterruptPhase::Confirmed {
        return Ok(IntentionalInterruptStep {
            state,
            signal: IntentionalInterruptSignal::None,
            fast_ready: false,
        });
    }
    if !aec_verified {
        state.phase = IntentionalInterruptPhase::LegacyOnly;
        return Ok(IntentionalInterruptStep {
            state,
            signal: IntentionalInterruptSignal::None,
            fast_ready: false,
        });
    }
    if candidate_elapsed_ms > 520 {
        state.phase = IntentionalInterruptPhase::LegacyOnly;
        return Ok(IntentionalInterruptStep {
            state,
            signal: IntentionalInterruptSignal::None,
            fast_ready: false,
        });
    }

    let guard_voiced = frame_flags & INTERRUPT_FRAME_GUARD_VOICED != 0;
    let voiced = frame_flags & INTERRUPT_FRAME_VOICED != 0;
    // During playback the guard bit is the classifier's strongest finite
    // threshold. Requiring it here prevents foreground-like residual speaker
    // leakage from entering the fast lane even when the browser reports AEC.
    let foreground = guard_voiced && frame_flags & INTERRUPT_FRAME_FOREGROUND_VOICED != 0;
    let bucket: u8 = if foreground {
        match (rms, peak) {
            (r, p) if r >= 0.075 || p >= 0.20 => 4,
            (r, p) if r >= 0.055 || p >= 0.15 => 3,
            (r, p) if r >= 0.038 || p >= 0.105 => 2,
            _ => 1,
        }
    } else {
        0
    };
    // A two-bucket jump is a bounded change-point. One-bucket motion is
    // treated as ordinary foreground so threshold jitter cannot manufacture
    // the multiple independent changes required by the upper boundary.
    let changed = foreground && state.last_bucket != 0 && bucket.abs_diff(state.last_bucket) >= 2;
    let signal = if changed {
        IntentionalInterruptSignal::ForegroundChange
    } else if foreground {
        IntentionalInterruptSignal::Foreground
    } else if voiced {
        IntentionalInterruptSignal::Voice
    } else {
        IntentionalInterruptSignal::None
    };

    match signal {
        IntentionalInterruptSignal::ForegroundChange => {
            state.score = (state.score + 6).min(32);
            state.change_count = state.change_count.saturating_add(1).min(8);
            state.foreground_ms = state.foreground_ms.saturating_add(u16::from(credited_ms));
            state.gap_ms = 0;
            state.last_bucket = bucket;
        }
        IntentionalInterruptSignal::Foreground => {
            state.score = (state.score + 2).min(32);
            state.foreground_ms = state.foreground_ms.saturating_add(u16::from(credited_ms));
            state.gap_ms = 0;
            state.last_bucket = bucket;
        }
        IntentionalInterruptSignal::Voice => {
            state.score = (state.score - 6).max(-24);
            state.phase = IntentionalInterruptPhase::LegacyOnly;
        }
        IntentionalInterruptSignal::None => {
            state.score = (state.score - 5).max(-24);
            let next_gap = state.gap_ms.saturating_add(credited_ms);
            if next_gap > 80 || state.score <= -12 {
                state.phase = IntentionalInterruptPhase::Discarded;
                state.gap_ms = 80;
            } else {
                state.gap_ms = next_gap;
            }
        }
    }

    if state.phase == IntentionalInterruptPhase::Armed && foreground {
        state.phase = IntentionalInterruptPhase::Candidate;
    }
    if state.phase == IntentionalInterruptPhase::Candidate
        && state.foreground_ms >= 160
        && state.change_count >= 1
        && state.score >= 10
    {
        state.phase = IntentionalInterruptPhase::Provisional;
    }
    let upper_boundary_reached = matches!(
        state.phase,
        IntentionalInterruptPhase::Candidate
            | IntentionalInterruptPhase::Provisional
            | IntentionalInterruptPhase::FastReady
    ) && state.foreground_ms >= 320
        && state.change_count >= 3
        && state.score >= 24
        && state.gap_ms <= 80;
    if upper_boundary_reached {
        state.phase = IntentionalInterruptPhase::FastReady;
    }
    let fast_ready = state.phase == IntentionalInterruptPhase::FastReady
        && (400..=520).contains(&candidate_elapsed_ms)
        && state.foreground_ms >= 320
        && state.change_count >= 3
        && state.score >= 24
        && state.gap_ms <= 80;
    if fast_ready {
        state.phase = IntentionalInterruptPhase::Confirmed;
    }
    Ok(IntentionalInterruptStep {
        state,
        signal,
        fast_ready,
    })
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct TemporalVadTick {
    pub credited_ms: f64,
    pub elapsed_ms: f64,
}

/// Advances VAD time from the audio device's monotonic sample clock.
///
/// A delayed JavaScript task may observe a large frame delta, but it must not
/// manufacture an equally large run of speech evidence from one acoustic
/// observation. Therefore elapsed time follows the sample clock while evidence
/// credit is capped at one 40 ms analysis interval. Duplicate and reverse
/// frames are rejected instead of silently inflating dwell thresholds.
pub fn advance_temporal_vad_clock(
    sample_rate_hz: u32,
    started_frame: u64,
    previous_frame: u64,
    current_frame: u64,
) -> Result<TemporalVadTick, DetectorError> {
    if !(8_000..=192_000).contains(&sample_rate_hz)
        || started_frame > previous_frame
        || current_frame <= previous_frame
    {
        return Err(DetectorError::InvalidTemporalVadClock);
    }
    let rate = f64::from(sample_rate_hz);
    let delta_ms = current_frame.saturating_sub(previous_frame) as f64 * 1_000.0 / rate;
    let elapsed_ms = current_frame.saturating_sub(started_frame) as f64 * 1_000.0 / rate;
    Ok(TemporalVadTick {
        credited_ms: delta_ms.min(TEMPORAL_VAD_MAXIMUM_TICK_MS),
        elapsed_ms,
    })
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct InterruptFrameLevels {
    pub noise_floor: f64,
    pub output_active: bool,
    pub peak: f64,
    pub rms: f64,
    pub candidate_active: bool,
}

/// Classifies one interruption-VAD frame without retaining PCM or features.
///
/// The bit contract is deliberately finite so the Wasm boundary cannot carry
/// raw audio. Time, density, and confirmation remain in the caller's bounded
/// state machine; this function is the single production implementation of
/// the acoustic threshold decision.
pub fn classify_interrupt_frame(levels: InterruptFrameLevels) -> Result<u8, DetectorError> {
    if !levels.noise_floor.is_finite()
        || !(0.0..=1.0).contains(&levels.noise_floor)
        || !levels.peak.is_finite()
        || !(0.0..=1.0).contains(&levels.peak)
        || !levels.rms.is_finite()
        || !(0.0..=1.0).contains(&levels.rms)
    {
        return Err(DetectorError::InvalidInterruptFrameLevels);
    }

    let guard_voiced = levels.rms
        >= (if levels.output_active {
            0.05_f64
        } else {
            0.03_f64
        })
        .max(levels.noise_floor * 4.5)
        && levels.peak
            >= (if levels.output_active {
                0.12_f64
            } else {
                0.08_f64
            })
            .max(levels.noise_floor * 9.0);
    let rms_threshold = (if levels.output_active {
        0.026_f64
    } else {
        0.014_f64
    })
    .max(levels.noise_floor * if levels.output_active { 3.2 } else { 2.35 });
    let peak_threshold = (if levels.output_active {
        0.065_f64
    } else {
        0.035_f64
    })
    .max(levels.noise_floor * if levels.output_active { 7.0 } else { 5.0 });
    let threshold_ratio = if levels.candidate_active { 0.86 } else { 1.0 };
    let voiced = levels.rms >= rms_threshold * threshold_ratio
        && levels.peak >= peak_threshold * threshold_ratio;
    let foreground_voiced = voiced
        && levels.rms
            >= (if levels.output_active {
                0.045_f64
            } else {
                0.026_f64
            })
            .max(levels.noise_floor * 5.0)
        && levels.peak
            >= (if levels.output_active {
                0.12_f64
            } else {
                0.07_f64
            })
            .max(levels.noise_floor * 9.0);

    Ok((if guard_voiced {
        INTERRUPT_FRAME_GUARD_VOICED
    } else {
        0
    }) | (if voiced { INTERRUPT_FRAME_VOICED } else { 0 })
        | (if foreground_voiced {
            INTERRUPT_FRAME_FOREGROUND_VOICED
        } else {
            0
        }))
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct DetectorConfig {
    pub sample_rate_hz: u32,
    pub frame_samples: usize,
    pub onset_frames: u16,
    pub offset_frames: u16,
    pub minimum_voice_dbfs: f32,
    pub noise_margin_db: f32,
    pub noise_learning_rate: f32,
}

impl Default for DetectorConfig {
    fn default() -> Self {
        Self {
            sample_rate_hz: 48_000,
            frame_samples: 480,
            onset_frames: 4,
            offset_frames: 18,
            minimum_voice_dbfs: -48.0,
            noise_margin_db: 9.0,
            noise_learning_rate: 0.04,
        }
    }
}

impl DetectorConfig {
    pub fn validate(self) -> Result<Self, DetectorError> {
        if self.sample_rate_hz < 8_000 || self.sample_rate_hz > 192_000 {
            return Err(DetectorError::InvalidConfiguration(
                "sample_rate_hz must be between 8000 and 192000",
            ));
        }
        if self.frame_samples == 0 {
            return Err(DetectorError::InvalidConfiguration(
                "frame_samples must not be zero",
            ));
        }
        if (self.frame_samples as u64).saturating_mul(1_000) < u64::from(self.sample_rate_hz) {
            return Err(DetectorError::InvalidConfiguration(
                "PCM frames must cover at least one millisecond",
            ));
        }
        if self.onset_frames == 0 || self.offset_frames == 0 {
            return Err(DetectorError::InvalidConfiguration(
                "onset_frames and offset_frames must not be zero",
            ));
        }
        if !(-90.0..=-6.0).contains(&self.minimum_voice_dbfs) {
            return Err(DetectorError::InvalidConfiguration(
                "minimum_voice_dbfs is outside the supported range",
            ));
        }
        if !(1.0..=30.0).contains(&self.noise_margin_db) {
            return Err(DetectorError::InvalidConfiguration(
                "noise_margin_db is outside the supported range",
            ));
        }
        if !(0.001..=0.25).contains(&self.noise_learning_rate) {
            return Err(DetectorError::InvalidConfiguration(
                "noise_learning_rate is outside the supported range",
            ));
        }
        Ok(self)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VoiceState {
    Listening,
    PossibleSpeech,
    Speaking,
    PossibleSilence,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct FrameAnalysis {
    pub state: VoiceState,
    pub elapsed_ms: u64,
    pub rms_dbfs: f32,
    pub peak: f32,
    pub adaptive_threshold_dbfs: f32,
    pub candidate_voice: bool,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TimingFeatures {
    pub elapsed_ms: u64,
    pub first_voice_ms: Option<u64>,
    pub voiced_ms: u64,
    pub trailing_silence_ms: u64,
    pub speech_segments: u16,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DetectorError {
    InvalidConfiguration(&'static str),
    WrongFrameLength { expected: usize, actual: usize },
    NonFiniteSample,
    InvalidInterruptFrameLevels,
    InvalidTemporalVadClock,
    InvalidIntentionalInterruptState,
}

impl fmt::Display for DetectorError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidConfiguration(message) => formatter.write_str(message),
            Self::WrongFrameLength { expected, actual } => {
                write!(
                    formatter,
                    "PCM frame length is {actual}; expected exactly {expected} samples"
                )
            }
            Self::NonFiniteSample => formatter.write_str("PCM frame contains a non-finite sample"),
            Self::InvalidInterruptFrameLevels => formatter
                .write_str("interruption frame levels must be finite values from zero to one"),
            Self::InvalidTemporalVadClock => formatter
                .write_str("temporal VAD clock must be monotonic and use a supported sample rate"),
            Self::InvalidIntentionalInterruptState => {
                formatter.write_str("intentional interruption state must be finite and bounded")
            }
        }
    }
}

impl std::error::Error for DetectorError {}

#[derive(Debug)]
pub struct VoiceDetector {
    config: DetectorConfig,
    processed_frames: u64,
    candidate_voice_frames: u16,
    candidate_silence_frames: u16,
    noise_floor_dbfs: Option<f32>,
    speaking: bool,
    features: TimingFeatures,
}

impl VoiceDetector {
    pub fn new(config: DetectorConfig) -> Result<Self, DetectorError> {
        Ok(Self {
            config: config.validate()?,
            processed_frames: 0,
            candidate_voice_frames: 0,
            candidate_silence_frames: 0,
            noise_floor_dbfs: None,
            speaking: false,
            features: TimingFeatures::default(),
        })
    }

    pub fn process_frame(&mut self, pcm: &[f32]) -> Result<FrameAnalysis, DetectorError> {
        if pcm.len() != self.config.frame_samples {
            return Err(DetectorError::WrongFrameLength {
                expected: self.config.frame_samples,
                actual: pcm.len(),
            });
        }

        let mut square_sum = 0.0_f64;
        let mut peak = 0.0_f32;
        for &sample in pcm {
            if !sample.is_finite() {
                return Err(DetectorError::NonFiniteSample);
            }
            let bounded = sample.clamp(-1.0, 1.0);
            square_sum += f64::from(bounded) * f64::from(bounded);
            peak = peak.max(bounded.abs());
        }

        let rms = (square_sum / pcm.len() as f64).sqrt() as f32;
        let rms_dbfs = if rms <= f32::EPSILON {
            SILENCE_DBFS
        } else {
            20.0 * rms.log10()
        };
        let noise_floor = self.noise_floor_dbfs.unwrap_or(SILENCE_DBFS);
        let threshold =
            (noise_floor + self.config.noise_margin_db).max(self.config.minimum_voice_dbfs);
        let candidate_voice = rms_dbfs >= threshold;

        self.processed_frames = self.processed_frames.saturating_add(1);
        self.features.elapsed_ms = self.frames_to_ms(self.processed_frames);

        if candidate_voice {
            self.candidate_silence_frames = 0;
            self.candidate_voice_frames = self.candidate_voice_frames.saturating_add(1);

            let mut onset_confirmed = false;
            if !self.speaking && self.candidate_voice_frames >= self.config.onset_frames {
                self.speaking = true;
                onset_confirmed = true;
                self.features.speech_segments = self.features.speech_segments.saturating_add(1);
                let onset_frame = self
                    .processed_frames
                    .saturating_sub(u64::from(self.config.onset_frames));
                let onset_ms = self.frames_to_ms(onset_frame);
                self.features.first_voice_ms.get_or_insert(onset_ms);
            }
            if self.speaking {
                let confirmed_frames = if onset_confirmed {
                    u64::from(self.config.onset_frames)
                } else {
                    1
                };
                self.features.voiced_ms = self
                    .features
                    .voiced_ms
                    .saturating_add(self.frame_duration_ms().saturating_mul(confirmed_frames));
                self.features.trailing_silence_ms = 0;
            }
        } else {
            self.candidate_voice_frames = 0;
            if self.speaking {
                self.candidate_silence_frames = self.candidate_silence_frames.saturating_add(1);
                self.features.trailing_silence_ms = self
                    .features
                    .trailing_silence_ms
                    .saturating_add(self.frame_duration_ms());
                if self.candidate_silence_frames >= self.config.offset_frames {
                    self.speaking = false;
                    self.candidate_silence_frames = 0;
                }
            } else {
                self.learn_noise_floor(rms_dbfs);
            }
        }

        Ok(FrameAnalysis {
            state: self.state(),
            elapsed_ms: self.features.elapsed_ms,
            rms_dbfs,
            peak,
            adaptive_threshold_dbfs: threshold,
            candidate_voice,
        })
    }

    pub fn features(&self) -> TimingFeatures {
        self.features
    }

    pub fn reset(&mut self) {
        self.processed_frames = 0;
        self.candidate_voice_frames = 0;
        self.candidate_silence_frames = 0;
        self.noise_floor_dbfs = None;
        self.speaking = false;
        self.features = TimingFeatures::default();
    }

    fn state(&self) -> VoiceState {
        if self.speaking {
            if self.candidate_silence_frames > 0 {
                VoiceState::PossibleSilence
            } else {
                VoiceState::Speaking
            }
        } else if self.candidate_voice_frames > 0 {
            VoiceState::PossibleSpeech
        } else {
            VoiceState::Listening
        }
    }

    fn learn_noise_floor(&mut self, rms_dbfs: f32) {
        self.noise_floor_dbfs = Some(match self.noise_floor_dbfs {
            None => rms_dbfs,
            Some(previous) => {
                previous
                    + self.config.noise_learning_rate
                        * (rms_dbfs.clamp(SILENCE_DBFS, self.config.minimum_voice_dbfs) - previous)
            }
        });
    }

    fn frame_duration_ms(&self) -> u64 {
        self.frames_to_ms(1)
    }

    fn frames_to_ms(&self, frames: u64) -> u64 {
        frames
            .saturating_mul(self.config.frame_samples as u64)
            .saturating_mul(1_000)
            / u64::from(self.config.sample_rate_hz)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn detector() -> VoiceDetector {
        VoiceDetector::new(DetectorConfig {
            sample_rate_hz: 10_000,
            frame_samples: 100,
            onset_frames: 2,
            offset_frames: 3,
            minimum_voice_dbfs: -40.0,
            noise_margin_db: 8.0,
            noise_learning_rate: 0.1,
        })
        .expect("valid detector")
    }

    #[test]
    fn extracts_first_voice_and_segment_without_retaining_pcm() {
        let mut detector = detector();
        let silence = [0.001; 100];
        let voice = [0.25; 100];

        for _ in 0..5 {
            detector.process_frame(&silence).expect("silence frame");
        }
        assert_eq!(
            detector
                .process_frame(&voice)
                .expect("voice candidate")
                .state,
            VoiceState::PossibleSpeech
        );
        assert_eq!(
            detector.process_frame(&voice).expect("speech onset").state,
            VoiceState::Speaking
        );

        let features = detector.features();
        assert_eq!(features.first_voice_ms, Some(50));
        assert_eq!(features.speech_segments, 1);
        assert!(features.voiced_ms > 0);
    }

    #[test]
    fn requires_sustained_silence_before_closing_a_segment() {
        let mut detector = detector();
        let voice = [0.25; 100];
        let silence = [0.0; 100];

        detector.process_frame(&voice).expect("candidate");
        detector.process_frame(&voice).expect("onset");
        detector.process_frame(&silence).expect("short silence");
        assert_eq!(detector.state(), VoiceState::PossibleSilence);
        detector.process_frame(&voice).expect("speech resumes");
        assert_eq!(detector.features().speech_segments, 1);

        for _ in 0..3 {
            detector.process_frame(&silence).expect("closing silence");
        }
        assert_eq!(detector.state(), VoiceState::Listening);
    }

    #[test]
    fn rejects_non_finite_samples() {
        let mut detector = detector();
        let mut frame = [0.0; 100];
        frame[3] = f32::NAN;
        assert_eq!(
            detector.process_frame(&frame),
            Err(DetectorError::NonFiniteSample)
        );
    }

    #[test]
    fn interruption_classifier_preserves_playback_and_candidate_thresholds() {
        let quiet = classify_interrupt_frame(InterruptFrameLevels {
            noise_floor: 0.004,
            output_active: false,
            peak: 0.035,
            rms: 0.014,
            candidate_active: false,
        })
        .expect("valid frame");
        assert_eq!(quiet & INTERRUPT_FRAME_VOICED, INTERRUPT_FRAME_VOICED);
        assert_eq!(quiet & INTERRUPT_FRAME_FOREGROUND_VOICED, 0);

        let playback_leak = classify_interrupt_frame(InterruptFrameLevels {
            noise_floor: 0.004,
            output_active: true,
            peak: 0.10,
            rms: 0.04,
            candidate_active: false,
        })
        .expect("valid frame");
        assert_eq!(
            playback_leak & INTERRUPT_FRAME_VOICED,
            INTERRUPT_FRAME_VOICED
        );
        assert_eq!(playback_leak & INTERRUPT_FRAME_GUARD_VOICED, 0);
        assert_eq!(playback_leak & INTERRUPT_FRAME_FOREGROUND_VOICED, 0);

        let continued = classify_interrupt_frame(InterruptFrameLevels {
            noise_floor: 0.004,
            output_active: true,
            peak: 0.056,
            rms: 0.023,
            candidate_active: true,
        })
        .expect("valid frame");
        assert_eq!(continued & INTERRUPT_FRAME_VOICED, INTERRUPT_FRAME_VOICED);
    }

    #[test]
    fn interruption_classifier_rejects_invalid_levels() {
        assert_eq!(
            classify_interrupt_frame(InterruptFrameLevels {
                noise_floor: 0.004,
                output_active: false,
                peak: f64::NAN,
                rms: 0.02,
                candidate_active: false,
            }),
            Err(DetectorError::InvalidInterruptFrameLevels)
        );
    }

    #[test]
    fn temporal_vad_clock_uses_sample_time_and_caps_single_tick_credit() {
        let tick = advance_temporal_vad_clock(48_000, 1_000, 1_480, 6_280)
            .expect("monotonic sample clock");
        assert_eq!(tick.credited_ms, 40.0);
        assert_eq!(tick.elapsed_ms, 110.0);
    }

    #[test]
    fn temporal_vad_clock_rejects_duplicate_reverse_and_invalid_rate() {
        assert_eq!(
            advance_temporal_vad_clock(48_000, 1_000, 2_000, 2_000),
            Err(DetectorError::InvalidTemporalVadClock)
        );
        assert_eq!(
            advance_temporal_vad_clock(48_000, 1_000, 2_000, 1_999),
            Err(DetectorError::InvalidTemporalVadClock)
        );
        assert_eq!(
            advance_temporal_vad_clock(1, 0, 0, 1),
            Err(DetectorError::InvalidTemporalVadClock)
        );
    }

    fn advance_fast_trace(
        trace: &[(u8, f64, f64, bool)],
    ) -> (IntentionalInterruptState, Option<u16>) {
        let mut state = IntentionalInterruptState::default();
        let mut decision = None;
        for (index, &(flags, rms, peak, aec)) in trace.iter().enumerate() {
            let elapsed = ((index + 1) * 40) as u16;
            let step = advance_intentional_interrupt(state, flags, rms, peak, 40, elapsed, aec)
                .expect("bounded synthetic state");
            state = step.state;
            if step.fast_ready {
                decision.get_or_insert(elapsed);
            }
        }
        (state, decision)
    }

    fn intentional_foreground_flags() -> u8 {
        INTERRUPT_FRAME_GUARD_VOICED | INTERRUPT_FRAME_VOICED | INTERRUPT_FRAME_FOREGROUND_VOICED
    }

    #[test]
    fn intentional_change_trace_is_ready_at_four_hundred_ms() {
        let trace: Vec<_> = (0..10)
            .map(|index| {
                let (rms, peak) = if index % 2 == 0 {
                    (0.045, 0.12)
                } else {
                    (0.08, 0.21)
                };
                (intentional_foreground_flags(), rms, peak, true)
            })
            .collect();
        let (state, decision) = advance_fast_trace(&trace);
        assert_eq!(decision, Some(400));
        assert_eq!(state.phase, IntentionalInterruptPhase::Confirmed);
        assert!(state.foreground_ms >= 320);
        assert!(state.change_count >= 3);
    }

    #[test]
    fn mutter_quiet_constant_and_aec_loss_never_fast_confirm() {
        let quiet = vec![(INTERRUPT_FRAME_VOICED, 0.02, 0.05, true); 15];
        assert_eq!(advance_fast_trace(&quiet).1, None);
        let constant = vec![(intentional_foreground_flags(), 0.06, 0.16, true,); 17];
        assert_eq!(advance_fast_trace(&constant).1, None);
        let mut lost_aec: Vec<_> = (0..13)
            .map(|index| {
                (
                    intentional_foreground_flags(),
                    if index % 2 == 0 { 0.045 } else { 0.08 },
                    if index % 2 == 0 { 0.12 } else { 0.21 },
                    index < 7,
                )
            })
            .collect();
        assert_eq!(advance_fast_trace(&lost_aec).1, None);
        lost_aec.clear();
    }

    #[test]
    fn constrained_h0_has_zero_fast_accepts_in_one_hundred_thousand_traces() {
        let mut false_accepts = 0_u32;
        for trace_id in 0..100_000_u32 {
            let mut trace = Vec::with_capacity(18);
            match trace_id % 6 {
                0 => {
                    // Cough/impulse: at most three strong, changing frames.
                    for index in 0..18 {
                        let foreground = index < 1 + (trace_id % 3) as usize;
                        trace.push(if foreground {
                            (
                                intentional_foreground_flags(),
                                if index % 2 == 0 { 0.045 } else { 0.08 },
                                if index % 2 == 0 { 0.12 } else { 0.21 },
                                true,
                            )
                        } else {
                            (0, 0.003, 0.006, true)
                        });
                    }
                }
                1 => trace.resize(18, (intentional_foreground_flags(), 0.06, 0.16, true)),
                // Quiet acknowledgement/mutter.
                2 => trace.resize(18, (INTERRUPT_FRAME_VOICED, 0.02, 0.05, true)),
                3 => {
                    // Sparse foreground with gaps beyond hysteresis.
                    for index in 0..18 {
                        trace.push(if index % 5 < 2 {
                            (
                                intentional_foreground_flags(),
                                if index % 2 == 0 { 0.045 } else { 0.08 },
                                if index % 2 == 0 { 0.12 } else { 0.21 },
                                true,
                            )
                        } else {
                            (0, 0.003, 0.006, true)
                        });
                    }
                }
                // Playback leakage: it can resemble foreground but does not
                // cross the classifier's strongest guard threshold.
                4 => trace.resize(
                    18,
                    (
                        INTERRUPT_FRAME_VOICED | INTERRUPT_FRAME_FOREGROUND_VOICED,
                        0.04,
                        0.10,
                        true,
                    ),
                ),
                _ => trace.resize(18, (intentional_foreground_flags(), 0.08, 0.21, false)),
            }
            false_accepts += u32::from(advance_fast_trace(&trace).1.is_some());
        }
        assert_eq!(false_accepts, 0);
        let one_sided_95_upper = 1.0_f64 - 0.05_f64.powf(1.0 / 100_000.0);
        assert!(one_sided_95_upper < 0.005);
    }

    #[test]
    fn intentional_synthetic_decision_p95_is_at_most_five_hundred_twenty_ms() {
        let mut decisions = Vec::with_capacity(1_000);
        for trace_id in 0..1_000_u16 {
            let trace: Vec<_> = (0..13)
                .map(|index| {
                    if trace_id % 4 == 0 && index == 5 {
                        return (0, 0.003, 0.006, true);
                    }
                    (
                        intentional_foreground_flags(),
                        if index % 2 == 0 { 0.045 } else { 0.08 },
                        if index % 2 == 0 { 0.12 } else { 0.21 },
                        true,
                    )
                })
                .collect();
            decisions.push(advance_fast_trace(&trace).1.expect("high-confidence trace"));
        }
        decisions.sort_unstable();
        let p95 = decisions[949];
        assert!(p95 <= 520);
        assert!(720_u16.saturating_sub(p95) >= 200);
    }

    #[test]
    fn duplicate_reverse_and_invalid_phase_fail_closed_without_evidence() {
        let initial = IntentionalInterruptState::default();
        let first = advance_intentional_interrupt(
            initial,
            intentional_foreground_flags(),
            0.045,
            0.12,
            40,
            40,
            true,
        )
        .expect("first monotonic tick");
        assert_eq!(
            advance_intentional_interrupt(
                first.state,
                intentional_foreground_flags(),
                0.08,
                0.21,
                40,
                40,
                true,
            ),
            Err(DetectorError::InvalidIntentionalInterruptState)
        );
        assert_eq!(
            advance_intentional_interrupt(
                first.state,
                intentional_foreground_flags(),
                0.08,
                0.21,
                40,
                20,
                true,
            ),
            Err(DetectorError::InvalidIntentionalInterruptState)
        );
        assert_eq!(
            IntentionalInterruptPhase::try_from(7),
            Err(DetectorError::InvalidIntentionalInterruptState)
        );
    }

    #[test]
    fn fast_ready_property_implies_all_safety_predicates() {
        let mut seed = 0x5eed_u64;
        for _ in 0..10_000 {
            let mut state = IntentionalInterruptState::default();
            for frame in 1..=13_u16 {
                seed = seed.wrapping_mul(6_364_136_223_846_793_005).wrapping_add(1);
                let aec = seed & 0x1f != 0;
                let foreground = seed & 3 != 0;
                let voiced = foreground || seed & 4 != 0;
                let flags = (u8::from(voiced) * INTERRUPT_FRAME_VOICED)
                    | (u8::from(foreground) * INTERRUPT_FRAME_GUARD_VOICED)
                    | (u8::from(foreground) * INTERRUPT_FRAME_FOREGROUND_VOICED);
                let rms = match (seed >> 8) & 3 {
                    0 => 0.03,
                    1 => 0.045,
                    2 => 0.06,
                    _ => 0.08,
                };
                let peak = rms * 2.5;
                let step =
                    advance_intentional_interrupt(state, flags, rms, peak, 40, frame * 40, aec)
                        .expect("generated input is bounded");
                state = step.state;
                if step.fast_ready {
                    assert!(aec);
                    assert!((400..=520).contains(&(frame * 40)));
                    assert!(state.foreground_ms >= 320);
                    assert!(state.change_count >= 3);
                    assert!(state.score >= 24);
                    assert!(state.gap_ms <= 80);
                    assert_eq!(state.phase, IntentionalInterruptPhase::Confirmed);
                }
            }
        }
    }
}
