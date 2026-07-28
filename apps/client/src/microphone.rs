//! Browser microphone capture for local-only timing analysis.
//!
//! This module deliberately does not expose recorded audio. A short-lived
//! `Float32` buffer is copied from Web Audio into `kotae_audio_core`, then
//! overwritten when the session stops. There is no `MediaRecorder`, `Blob`,
//! storage, or network path here.

use core::fmt;
use kotae_audio_core::TimingFeatures;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MicrophoneError {
    Unsupported,
    PermissionDenied,
    DeviceUnavailable,
    CaptureFailed,
    AudioGraphFailed,
    DetectorFailed,
}

impl MicrophoneError {
    pub const fn code(self) -> &'static str {
        match self {
            Self::Unsupported => "unsupported",
            Self::PermissionDenied => "permission-denied",
            Self::DeviceUnavailable => "device-unavailable",
            Self::CaptureFailed => "capture-failed",
            Self::AudioGraphFailed => "audio-graph-failed",
            Self::DetectorFailed => "detector-failed",
        }
    }
}

impl fmt::Display for MicrophoneError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.code())
    }
}

impl std::error::Error for MicrophoneError {}

/// An active local microphone analysis session.
///
/// Call [`stop`](Self::stop) to release the microphone immediately. Dropping
/// the value performs the same synchronous cleanup as a safety net.
pub struct MicrophoneSession {
    inner: platform::Session,
}

impl MicrophoneSession {
    /// Ask for an audio-only stream and begin local timing analysis.
    ///
    /// This must be called from a user gesture on browsers that enforce
    /// autoplay restrictions.
    pub async fn start() -> Result<Self, MicrophoneError> {
        platform::Session::start().await.map(|inner| Self { inner })
    }

    pub fn is_active(&self) -> bool {
        self.inner.is_active()
    }

    pub fn features(&self) -> TimingFeatures {
        self.inner.features()
    }

    /// Returns an analysis error raised after capture started, if any.
    pub fn analysis_error(&self) -> Option<MicrophoneError> {
        self.inner.analysis_error()
    }

    /// Stop capture, release browser resources, and return the final features.
    pub fn stop(&mut self) -> TimingFeatures {
        self.inner.stop()
    }
}

#[cfg(target_arch = "wasm32")]
mod platform {
    use super::MicrophoneError;
    use js_sys::{Array, Reflect};
    use kotae_audio_core::{DetectorConfig, TimingFeatures, VoiceDetector};
    use std::cell::{Cell, RefCell};
    use std::rc::Rc;
    use wasm_bindgen::{JsCast, JsValue, closure::Closure};
    use wasm_bindgen_futures::JsFuture;
    use web_sys::{
        AnalyserNode, AudioContext, AudioNode, MediaStream, MediaStreamAudioSourceNode,
        MediaStreamConstraints, MediaStreamTrack, Window,
    };

    const TARGET_FRAME_MS: f32 = 25.0;
    const MIN_FRAME_MS: f32 = 20.0;
    const MAX_FRAME_MS: f32 = 30.0;

    struct AnalysisState {
        detector: VoiceDetector,
        error: Option<MicrophoneError>,
    }

    pub(super) struct Session {
        window: Window,
        context: AudioContext,
        source: MediaStreamAudioSourceNode,
        analyser: AnalyserNode,
        tracks: Rc<RefCell<Vec<MediaStreamTrack>>>,
        pcm: Rc<RefCell<Vec<f32>>>,
        analysis: Rc<RefCell<AnalysisState>>,
        interval_id: Rc<Cell<Option<i32>>>,
        interval: Option<Closure<dyn FnMut()>>,
        deadline_id: Option<i32>,
        deadline: Option<Closure<dyn FnMut()>>,
        active: Rc<Cell<bool>>,
    }

    impl Session {
        pub(super) async fn start() -> Result<Self, MicrophoneError> {
            let window = web_sys::window().ok_or(MicrophoneError::Unsupported)?;
            let media_devices = window
                .navigator()
                .media_devices()
                .map_err(|_| MicrophoneError::Unsupported)?;

            let constraints = MediaStreamConstraints::new();
            constraints.set_audio_bool(true);
            constraints.set_video_bool(false);

            let capture = media_devices
                .get_user_media_with_constraints(&constraints)
                .map_err(classify_capture_error)?;
            let stream_value = JsFuture::from(capture)
                .await
                .map_err(classify_capture_error)?;
            let stream = stream_value
                .dyn_into::<MediaStream>()
                .map_err(|_| MicrophoneError::CaptureFailed)?;

            let tracks = media_tracks(&stream);
            if tracks.is_empty() || stream.get_video_tracks().length() != 0 {
                stop_tracks(&tracks);
                return Err(MicrophoneError::DeviceUnavailable);
            }

            let context = match AudioContext::new() {
                Ok(context) => context,
                Err(_) => {
                    stop_tracks(&tracks);
                    return Err(MicrophoneError::AudioGraphFailed);
                }
            };

            let result = Self::build(window, context.clone(), stream, tracks).await;
            if result.is_err() {
                let _ = context.close();
            }
            result
        }

        async fn build(
            window: Window,
            context: AudioContext,
            stream: MediaStream,
            tracks: Vec<MediaStreamTrack>,
        ) -> Result<Self, MicrophoneError> {
            match context.resume() {
                Ok(resume) => {
                    if JsFuture::from(resume).await.is_err() {
                        stop_tracks(&tracks);
                        return Err(MicrophoneError::AudioGraphFailed);
                    }
                }
                Err(_) => {
                    stop_tracks(&tracks);
                    return Err(MicrophoneError::AudioGraphFailed);
                }
            }

            let sample_rate = checked_sample_rate(context.sample_rate()).ok_or_else(|| {
                stop_tracks(&tracks);
                MicrophoneError::AudioGraphFailed
            })?;
            let frame_samples = choose_frame_samples(sample_rate).ok_or_else(|| {
                stop_tracks(&tracks);
                MicrophoneError::AudioGraphFailed
            })?;

            let source = context.create_media_stream_source(&stream).map_err(|_| {
                stop_tracks(&tracks);
                MicrophoneError::AudioGraphFailed
            })?;
            let analyser = context.create_analyser().map_err(|_| {
                stop_tracks(&tracks);
                MicrophoneError::AudioGraphFailed
            })?;
            analyser.set_fft_size(frame_samples as u32);
            source.connect_with_audio_node(&analyser).map_err(|_| {
                stop_tracks(&tracks);
                MicrophoneError::AudioGraphFailed
            })?;

            let detector = VoiceDetector::new(DetectorConfig {
                sample_rate_hz: sample_rate,
                frame_samples,
                ..DetectorConfig::default()
            })
            .map_err(|_| {
                stop_tracks(&tracks);
                let _ = source.disconnect();
                MicrophoneError::DetectorFailed
            })?;

            let pcm = Rc::new(RefCell::new(vec![0.0; frame_samples]));
            let analysis = Rc::new(RefCell::new(AnalysisState {
                detector,
                error: None,
            }));
            let callback_pcm = Rc::clone(&pcm);
            let callback_analysis = Rc::clone(&analysis);
            let callback_analyser = analyser.clone();
            let interval = Closure::wrap(Box::new(move || {
                let mut pcm = callback_pcm.borrow_mut();
                callback_analyser.get_float_time_domain_data(pcm.as_mut_slice());
                let mut state = callback_analysis.borrow_mut();
                if state.error.is_none() && state.detector.process_frame(&pcm).is_err() {
                    state.error = Some(MicrophoneError::DetectorFailed);
                }
            }) as Box<dyn FnMut()>);

            let interval_ms =
                ((frame_samples as f32 * 1_000.0) / sample_rate as f32).round() as i32;
            let interval_id = window
                .set_interval_with_callback_and_timeout_and_arguments_0(
                    interval.as_ref().unchecked_ref(),
                    interval_ms,
                )
                .map_err(|_| {
                    stop_tracks(&tracks);
                    let _ = source.disconnect();
                    MicrophoneError::AudioGraphFailed
                })?;
            let interval_id = Rc::new(Cell::new(Some(interval_id)));
            let tracks = Rc::new(RefCell::new(tracks));
            let active = Rc::new(Cell::new(true));

            // Capture has its own hard upper bound. The UI also observes this
            // state, but the microphone is released here even if that UI task
            // is cancelled or fails to render.
            let deadline_window = window.clone();
            let deadline_context = context.clone();
            let deadline_source = source.clone();
            let deadline_analyser = analyser.clone();
            let deadline_tracks = Rc::clone(&tracks);
            let deadline_pcm = Rc::clone(&pcm);
            let deadline_interval_id = Rc::clone(&interval_id);
            let deadline_active = Rc::clone(&active);
            let deadline = Closure::wrap(Box::new(move || {
                release_capture(
                    &deadline_window,
                    &deadline_context,
                    &deadline_source,
                    &deadline_analyser,
                    &deadline_tracks,
                    &deadline_pcm,
                    &deadline_interval_id,
                    &deadline_active,
                );
            }) as Box<dyn FnMut()>);
            let deadline_id = window
                .set_timeout_with_callback_and_timeout_and_arguments_0(
                    deadline.as_ref().unchecked_ref(),
                    10_000,
                )
                .map_err(|_| {
                    release_capture(
                        &window,
                        &context,
                        &source,
                        &analyser,
                        &tracks,
                        &pcm,
                        &interval_id,
                        &active,
                    );
                    MicrophoneError::AudioGraphFailed
                })?;

            Ok(Self {
                window,
                context,
                source,
                analyser,
                tracks,
                pcm,
                analysis,
                interval_id,
                interval: Some(interval),
                deadline_id: Some(deadline_id),
                deadline: Some(deadline),
                active,
            })
        }

        pub(super) fn is_active(&self) -> bool {
            self.active.get()
        }

        pub(super) fn features(&self) -> TimingFeatures {
            self.analysis.borrow().detector.features()
        }

        pub(super) fn analysis_error(&self) -> Option<MicrophoneError> {
            self.analysis.borrow().error
        }

        pub(super) fn stop(&mut self) -> TimingFeatures {
            if let Some(deadline_id) = self.deadline_id.take() {
                self.window.clear_timeout_with_handle(deadline_id);
            }
            self.deadline.take();
            release_capture(
                &self.window,
                &self.context,
                &self.source,
                &self.analyser,
                &self.tracks,
                &self.pcm,
                &self.interval_id,
                &self.active,
            );
            self.interval.take();
            self.features()
        }
    }

    impl Drop for Session {
        fn drop(&mut self) {
            self.stop();
        }
    }

    fn checked_sample_rate(sample_rate: f32) -> Option<u32> {
        if sample_rate.is_finite() && (8_000.0..=192_000.0).contains(&sample_rate) {
            Some(sample_rate.round() as u32)
        } else {
            None
        }
    }

    fn choose_frame_samples(sample_rate: u32) -> Option<usize> {
        (5_u32..=15)
            .map(|power| 1_usize << power)
            .filter(|samples| {
                let duration_ms = *samples as f32 * 1_000.0 / sample_rate as f32;
                (MIN_FRAME_MS..=MAX_FRAME_MS).contains(&duration_ms)
            })
            .min_by(|left, right| {
                let left_delta =
                    ((*left as f32 * 1_000.0 / sample_rate as f32) - TARGET_FRAME_MS).abs();
                let right_delta =
                    ((*right as f32 * 1_000.0 / sample_rate as f32) - TARGET_FRAME_MS).abs();
                left_delta.total_cmp(&right_delta)
            })
    }

    fn media_tracks(stream: &MediaStream) -> Vec<MediaStreamTrack> {
        let values: Array = stream.get_audio_tracks();
        values
            .iter()
            .filter_map(|value| value.dyn_into::<MediaStreamTrack>().ok())
            .collect()
    }

    fn stop_tracks(tracks: &[MediaStreamTrack]) {
        for track in tracks {
            track.stop();
        }
    }

    fn release_capture(
        window: &Window,
        context: &AudioContext,
        source: &MediaStreamAudioSourceNode,
        analyser: &AnalyserNode,
        tracks: &Rc<RefCell<Vec<MediaStreamTrack>>>,
        pcm: &Rc<RefCell<Vec<f32>>>,
        interval_id: &Rc<Cell<Option<i32>>>,
        active: &Rc<Cell<bool>>,
    ) {
        if let Some(interval_id) = interval_id.take() {
            window.clear_interval_with_handle(interval_id);
        }
        if active.replace(false) {
            stop_tracks(&tracks.borrow());
            tracks.borrow_mut().clear();
            let _ = source.disconnect();
            let _ = analyser.disconnect();
            let _ = context.close();
        }
        pcm.borrow_mut().fill(0.0);
    }

    fn classify_capture_error(error: JsValue) -> MicrophoneError {
        let name = Reflect::get(&error, &JsValue::from_str("name"))
            .ok()
            .and_then(|value| value.as_string());
        match name.as_deref() {
            Some("NotAllowedError" | "SecurityError") => MicrophoneError::PermissionDenied,
            Some("NotFoundError" | "OverconstrainedError" | "NotReadableError") => {
                MicrophoneError::DeviceUnavailable
            }
            _ => MicrophoneError::CaptureFailed,
        }
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod platform {
    use super::MicrophoneError;
    use kotae_audio_core::TimingFeatures;

    pub(super) struct Session;

    impl Session {
        pub(super) async fn start() -> Result<Self, MicrophoneError> {
            Err(MicrophoneError::Unsupported)
        }

        pub(super) const fn is_active(&self) -> bool {
            false
        }

        pub(super) const fn features(&self) -> TimingFeatures {
            TimingFeatures {
                elapsed_ms: 0,
                first_voice_ms: None,
                voiced_ms: 0,
                trailing_silence_ms: 0,
                speech_segments: 0,
            }
        }

        pub(super) const fn analysis_error(&self) -> Option<MicrophoneError> {
            None
        }

        pub(super) fn stop(&mut self) -> TimingFeatures {
            self.features()
        }
    }
}
