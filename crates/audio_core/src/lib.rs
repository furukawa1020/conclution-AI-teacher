//! Local voice timing analysis for KOTAE AI.
//!
//! This crate deliberately produces timing features rather than retaining raw
//! samples. Platform layers own microphone access and feed normalized mono PCM
//! frames into [`VoiceDetector`].

use core::fmt;

const SILENCE_DBFS: f32 = -120.0;

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
}
