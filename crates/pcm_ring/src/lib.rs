//! A finite PCM16 frame ring for the browser AudioWorklet boundary.
//!
//! The ring owns every completed frame while it is awaiting confirmation or
//! transport credit. Entries are wiped before eviction, removal, clear, and
//! drop. It deliberately exports no inspection API for retained audio.

use zeroize::Zeroize;

pub const FRAME_BYTES: usize = 640;
pub const TURN_REFERENCE_MAXIMUM_FRAMES: usize = 20;
const TURN_REFERENCE_BANDS_HZ: [f64; 8] = [
    250.0, 500.0, 750.0, 1_000.0, 1_500.0, 2_000.0, 3_000.0, 4_000.0,
];
/// Three minutes and thirty seconds of 20 ms PCM frames. This is the hard
/// browser voice-turn ceiling; callers may choose a smaller queue.
pub const MAXIMUM_CAPACITY: usize = 10_500;
#[cfg(any(test, target_arch = "wasm32"))]
const MAXIMUM_SAFE_JS_INTEGER: f64 = 9_007_199_254_740_991.0;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum OverflowPolicy {
    Reject,
    OverwriteOldest,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum PushResult {
    Invalid = 0,
    Inserted = 1,
    InsertedAfterEviction = 2,
    Full = 3,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum QuietCompensationResult {
    Invalid = 0,
    Unchanged = 1,
    Compensated = 2,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[repr(u8)]
pub enum QuietPhaseIntegrity {
    Invalid = 0,
    Consistent = 1,
    InsufficientSupport = 2,
    NonCausalOnset = 3,
    ExcessGroupDelay = 4,
    TemporalSmear = 5,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[repr(u8)]
pub enum TurnReferencePhase {
    Collecting = 0,
    Ready = 1,
    Unresolved = 2,
    Cleared = 3,
}

/// Owns at most 400 ms of non-exported, turn-local reference features.
///
/// This is the capability boundary for a future embedding-free cross-attention
/// model. It deliberately exposes neither PCM nor feature values and cannot be
/// reused across a generation. Any ambiguity wipes all retained support.
pub struct TurnReferenceWindow {
    generation: u64,
    context_sample_rate_hz: u32,
    last_context_frame: Option<u64>,
    features: [[i16; 8]; TURN_REFERENCE_MAXIMUM_FRAMES],
    count: usize,
    phase: TurnReferencePhase,
}

impl TurnReferenceWindow {
    pub fn new(generation: u64, context_sample_rate_hz: u32) -> Result<Self, &'static str> {
        if generation == 0 || !(16_000..=192_000).contains(&context_sample_rate_hz) {
            return Err("invalid_turn_reference");
        }
        Ok(Self {
            generation,
            context_sample_rate_hz,
            last_context_frame: None,
            features: [[0_i16; 8]; TURN_REFERENCE_MAXIMUM_FRAMES],
            count: 0,
            phase: TurnReferencePhase::Collecting,
        })
    }

    pub fn phase(&self, generation: u64) -> TurnReferencePhase {
        if generation == self.generation {
            self.phase
        } else {
            TurnReferencePhase::Unresolved
        }
    }

    pub fn count(&self, generation: u64) -> Option<usize> {
        (generation == self.generation).then_some(self.count)
    }

    #[allow(clippy::too_many_arguments)]
    pub fn advance(
        &mut self,
        generation: u64,
        context_frame: u64,
        pcm: &[u8],
        quiet_confirmed: bool,
        aec_verified: bool,
        output_active: bool,
        overlap: bool,
    ) -> TurnReferencePhase {
        let maximum_gap = u64::from(self.context_sample_rate_hz) / 10;
        let invalid = generation != self.generation
            || self.phase != TurnReferencePhase::Collecting
            || pcm.len() != FRAME_BYTES
            || !quiet_confirmed
            || !aec_verified
            || output_active
            || overlap
            || self.last_context_frame.is_some_and(|previous| {
                context_frame <= previous || context_frame - previous > maximum_gap
            });
        if invalid {
            self.resolve_as_unresolved();
            return self.phase;
        }

        let Some(feature) = turn_reference_feature(pcm) else {
            self.resolve_as_unresolved();
            return self.phase;
        };
        self.features[self.count] = feature;
        self.count += 1;
        self.last_context_frame = Some(context_frame);
        if self.count == TURN_REFERENCE_MAXIMUM_FRAMES {
            self.phase = TurnReferencePhase::Ready;
        }
        self.phase
    }

    pub fn clear(&mut self, generation: u64) -> bool {
        if generation != self.generation {
            self.resolve_as_unresolved();
            return false;
        }
        self.wipe();
        self.phase = TurnReferencePhase::Cleared;
        true
    }

    fn resolve_as_unresolved(&mut self) {
        self.wipe();
        self.phase = TurnReferencePhase::Unresolved;
    }

    fn wipe(&mut self) {
        self.features.zeroize();
        self.count = 0;
        self.last_context_frame = None;
    }
}

impl Drop for TurnReferenceWindow {
    fn drop(&mut self) {
        self.wipe();
        self.generation = 0;
        self.context_sample_rate_hz = 0;
        self.phase = TurnReferencePhase::Cleared;
    }
}

fn turn_reference_feature(pcm: &[u8]) -> Option<[i16; 8]> {
    if pcm.len() != FRAME_BYTES {
        return None;
    }
    let mut samples = [0.0_f64; FRAME_BYTES / 2];
    let mut total_energy = 0.0_f64;
    for (target, bytes) in samples.iter_mut().zip(pcm.chunks_exact(2)) {
        *target = f64::from(i16::from_le_bytes([bytes[0], bytes[1]])) / 32_768.0;
        total_energy += *target * *target;
    }
    if !total_energy.is_finite() || total_energy <= 1.0e-12 {
        samples.zeroize();
        return None;
    }
    let mut energy = [0.0_f64; 8];
    for (band, frequency) in TURN_REFERENCE_BANDS_HZ.iter().enumerate() {
        let omega = core::f64::consts::TAU * frequency / 16_000.0;
        let mut real = 0.0_f64;
        let mut imaginary = 0.0_f64;
        for (index, sample) in samples.iter().enumerate() {
            let angle = omega * index as f64;
            real += sample * angle.cos();
            imaginary -= sample * angle.sin();
        }
        energy[band] = real.mul_add(real, imaginary * imaginary).max(0.0);
    }
    let supported = energy.iter().sum::<f64>();
    if !supported.is_finite() || supported <= 1.0e-12 {
        samples.zeroize();
        energy.zeroize();
        return None;
    }
    let mut feature = [0_i16; 8];
    for (target, value) in feature.iter_mut().zip(energy.iter()) {
        *target = ((*value / supported).sqrt() * f64::from(i16::MAX))
            .round()
            .clamp(0.0, f64::from(i16::MAX)) as i16;
    }
    samples.zeroize();
    energy.zeroize();
    Some(feature)
}

const QUIET_TARGET_RMS: f64 = 0.055;
const QUIET_MINIMUM_RMS: f64 = 0.0015;
const QUIET_PEAK_CEILING: f64 = 0.82;
const QUIET_MAXIMUM_GAIN: f64 = 4.0;
const QUIET_MAXIMUM_CREST_FACTOR: f64 = 8.0;
const QUIET_GAIN_ATTACK: f64 = 0.75;
const QUIET_GAIN_RELEASE: f64 = 0.2;
const DC_BLOCK_POLE: f64 = 0.995;
const QUIET_PRE_EMPHASIS: f64 = 0.28;
const OBSERVATION_MINIMUM_MIX: f64 = 0.30;
const OBSERVATION_MAXIMUM_MIX: f64 = 0.40;
const OBSERVATION_MINIMUM_CORRELATION: f64 = 0.35;
const OBSERVATION_MAXIMUM_RESIDUAL_RATIO: f64 = 16.0;
const PHASE_SAMPLE_RATE_HZ: f64 = 16_000.0;
const PHASE_FREQUENCIES_HZ: [f64; 6] = [250.0, 500.0, 1_000.0, 1_800.0, 2_800.0, 4_000.0];
const PHASE_MINIMUM_SUPPORTED_BANDS: usize = 2;
const PHASE_MAXIMUM_ONSET_ADVANCE_SAMPLES: usize = 1;
const PHASE_MAXIMUM_SMEAR_SAMPLES: usize = 12;
const PHASE_MAXIMUM_GROUP_DELAY_SAMPLES: f64 = 8.0;

struct QuietSpectralState {
    gain: f64,
    observation_mix: f64,
    previous_input: f64,
    previous_dc: f64,
    phase_integrity: QuietPhaseIntegrity,
}

impl Zeroize for QuietSpectralState {
    fn zeroize(&mut self) {
        self.gain = 0.0;
        self.observation_mix = 0.0;
        self.previous_input = 0.0;
        self.previous_dc = 0.0;
        self.phase_integrity = QuietPhaseIntegrity::Invalid;
    }
}

impl QuietSpectralState {
    fn new() -> Self {
        Self {
            gain: 1.0,
            observation_mix: OBSERVATION_MAXIMUM_MIX,
            previous_input: 0.0,
            previous_dc: 0.0,
            phase_integrity: QuietPhaseIntegrity::Invalid,
        }
    }

    fn reset(&mut self) {
        self.zeroize();
        self.gain = 1.0;
        self.observation_mix = OBSERVATION_MAXIMUM_MIX;
    }
}

struct Slot {
    occupied: bool,
    context_frame: u64,
    pcm: [u8; FRAME_BYTES],
}

impl Slot {
    fn empty() -> Self {
        Self {
            occupied: false,
            context_frame: 0,
            pcm: [0_u8; FRAME_BYTES],
        }
    }

    fn wipe(&mut self) {
        self.pcm.zeroize();
        self.context_frame = 0;
        self.occupied = false;
    }
}

pub struct PcmRing {
    generation: u64,
    slots: Vec<Slot>,
    head: usize,
    count: usize,
    last_context_frame: Option<u64>,
    last_weak_context_frame: Option<u64>,
    overflow_policy: OverflowPolicy,
    quiet_spectral: QuietSpectralState,
}

impl PcmRing {
    pub fn new(
        generation: u64,
        capacity: usize,
        overflow_policy: OverflowPolicy,
    ) -> Result<Self, &'static str> {
        if generation == 0 {
            return Err("invalid_generation");
        }
        if capacity == 0 || capacity > MAXIMUM_CAPACITY {
            return Err("invalid_capacity");
        }
        Ok(Self {
            generation,
            slots: (0..capacity).map(|_| Slot::empty()).collect(),
            head: 0,
            count: 0,
            last_context_frame: None,
            last_weak_context_frame: None,
            overflow_policy,
            quiet_spectral: QuietSpectralState::new(),
        })
    }

    pub fn capacity(&self) -> usize {
        self.slots.len()
    }

    pub fn generation(&self) -> u64 {
        self.generation
    }

    pub fn count(&self, generation: u64) -> Option<usize> {
        (generation == self.generation).then_some(self.count)
    }

    pub fn quiet_phase_integrity(&self, generation: u64) -> Option<QuietPhaseIntegrity> {
        (generation == self.generation).then_some(self.quiet_spectral.phase_integrity)
    }

    pub fn is_full(&self) -> bool {
        self.count == self.capacity()
    }

    fn push_frame(&mut self, context_frame: u64, pcm: &[u8]) -> PushResult {
        if pcm.len() != FRAME_BYTES
            || self
                .last_context_frame
                .is_some_and(|previous| context_frame <= previous)
        {
            return PushResult::Invalid;
        }
        if self.is_full() {
            if self.overflow_policy == OverflowPolicy::Reject {
                return PushResult::Full;
            }
            let slot = &mut self.slots[self.head];
            debug_assert!(slot.occupied);
            slot.wipe();
            slot.pcm.copy_from_slice(pcm);
            slot.context_frame = context_frame;
            slot.occupied = true;
            self.head = (self.head + 1) % self.capacity();
            self.last_context_frame = Some(context_frame);
            return PushResult::InsertedAfterEviction;
        }

        let index = (self.head + self.count) % self.capacity();
        let slot = &mut self.slots[index];
        debug_assert!(!slot.occupied);
        slot.pcm.copy_from_slice(pcm);
        slot.context_frame = context_frame;
        slot.occupied = true;
        self.count += 1;
        self.last_context_frame = Some(context_frame);
        PushResult::Inserted
    }

    pub fn push(&mut self, generation: u64, context_frame: u64, pcm: &[u8]) -> PushResult {
        if generation != self.generation {
            return PushResult::Invalid;
        }
        self.push_frame(context_frame, pcm)
    }

    pub fn shift_into(&mut self, generation: u64, destination: &mut [u8]) -> Option<u64> {
        if generation != self.generation || destination.len() != FRAME_BYTES || self.count == 0 {
            return None;
        }
        let slot = &mut self.slots[self.head];
        if !slot.occupied {
            return None;
        }
        destination.copy_from_slice(&slot.pcm);
        let context_frame = slot.context_frame;
        slot.wipe();
        self.head = (self.head + 1) % self.capacity();
        self.count -= 1;
        if self.count == 0 {
            self.head = 0;
        }
        Some(context_frame)
    }

    /// Derive the weak observation for three-view ASR while binding it to the
    /// same turn generation and strictly increasing browser sample clock.
    ///
    /// Invalid or stale input leaves the destination and binding clock intact.
    pub fn derive_weak_frame(
        &mut self,
        generation: u64,
        context_frame: u64,
        baseline: &[u8],
        enhanced: &[u8],
        destination: &mut [u8],
    ) -> bool {
        if generation != self.generation
            || baseline.len() != FRAME_BYTES
            || enhanced.len() != FRAME_BYTES
            || destination.len() != FRAME_BYTES
            || self
                .last_weak_context_frame
                .is_some_and(|previous| context_frame <= previous)
        {
            return false;
        }

        let mut weak = [0_u8; FRAME_BYTES];
        for ((raw, strong), output) in baseline
            .chunks_exact(2)
            .zip(enhanced.chunks_exact(2))
            .zip(weak.chunks_exact_mut(2))
        {
            let raw = i32::from(i16::from_le_bytes([raw[0], raw[1]]));
            let strong = i32::from(i16::from_le_bytes([strong[0], strong[1]]));
            let sample =
                ((3 * raw + strong) / 4).clamp(i32::from(i16::MIN), i32::from(i16::MAX)) as i16;
            output.copy_from_slice(&sample.to_le_bytes());
        }
        destination.copy_from_slice(&weak);
        weak.zeroize();
        self.last_weak_context_frame = Some(context_frame);
        true
    }

    pub fn clear(&mut self, generation: u64) -> bool {
        if generation != self.generation {
            return false;
        }
        self.wipe_all();
        true
    }

    fn wipe_all(&mut self) {
        self.slots.iter_mut().for_each(Slot::wipe);
        self.head = 0;
        self.count = 0;
        self.last_context_frame = None;
        self.last_weak_context_frame = None;
        self.quiet_spectral.reset();
    }

    pub fn compensate_quiet_frame(
        &mut self,
        generation: u64,
        pcm: &mut [u8],
    ) -> QuietCompensationResult {
        if generation != self.generation || pcm.len() != FRAME_BYTES {
            return QuietCompensationResult::Invalid;
        }

        let mut sum_squares = 0.0_f64;
        let mut peak = 0.0_f64;
        for bytes in pcm.chunks_exact(2) {
            let sample = f64::from(i16::from_le_bytes([bytes[0], bytes[1]])) / 32_768.0;
            sum_squares += sample * sample;
            peak = peak.max(sample.abs());
        }
        let rms = (sum_squares / (FRAME_BYTES / 2) as f64).sqrt();
        if !rms.is_finite() || !peak.is_finite() {
            return QuietCompensationResult::Invalid;
        }
        if rms < QUIET_MINIMUM_RMS || peak == 0.0 {
            self.quiet_spectral.reset();
            return QuietCompensationResult::Unchanged;
        }
        if rms >= QUIET_TARGET_RMS
            || peak >= QUIET_PEAK_CEILING
            || peak / rms > QUIET_MAXIMUM_CREST_FACTOR
        {
            self.quiet_spectral.reset();
            return QuietCompensationResult::Unchanged;
        }

        let requested_gain = (QUIET_TARGET_RMS / rms)
            .min(QUIET_PEAK_CEILING / peak)
            .clamp(1.0, QUIET_MAXIMUM_GAIN);
        let smoothing = if requested_gain > self.quiet_spectral.gain {
            QUIET_GAIN_ATTACK
        } else {
            QUIET_GAIN_RELEASE
        };
        let smoothed_gain =
            self.quiet_spectral.gain + (requested_gain - self.quiet_spectral.gain) * smoothing;

        let mut raw = [0.0_f64; FRAME_BYTES / 2];
        let mut filtered = [0.0_f64; FRAME_BYTES / 2];
        let mut previous_input = self.quiet_spectral.previous_input;
        let mut previous_dc = self.quiet_spectral.previous_dc;
        let mut filtered_peak = 0.0_f64;
        for (index, bytes) in pcm.chunks_exact(2).enumerate() {
            let input = f64::from(i16::from_le_bytes([bytes[0], bytes[1]])) / 32_768.0;
            raw[index] = input;
            let dc = input - previous_input + DC_BLOCK_POLE * previous_dc;
            let tilted = dc + QUIET_PRE_EMPHASIS * (dc - previous_dc);
            if !tilted.is_finite() {
                raw.zeroize();
                filtered.zeroize();
                return QuietCompensationResult::Invalid;
            }
            filtered[index] = tilted;
            filtered_peak = filtered_peak.max(tilted.abs());
            previous_input = input;
            previous_dc = dc;
        }
        if filtered_peak == 0.0 || !filtered_peak.is_finite() {
            raw.zeroize();
            filtered.zeroize();
            return QuietCompensationResult::Invalid;
        }
        let applied_gain = smoothed_gain
            .min(QUIET_PEAK_CEILING / filtered_peak)
            .clamp(0.0, QUIET_MAXIMUM_GAIN);
        if !applied_gain.is_finite() {
            raw.zeroize();
            filtered.zeroize();
            return QuietCompensationResult::Invalid;
        }

        let mut raw_energy = 0.0_f64;
        let mut enhanced_energy = 0.0_f64;
        let mut cross_energy = 0.0_f64;
        let mut residual_energy = 0.0_f64;
        for (observation, enhanced) in raw.iter().zip(filtered.iter_mut()) {
            *enhanced *= applied_gain;
            raw_energy += observation * observation;
            enhanced_energy += *enhanced * *enhanced;
            cross_energy += observation * *enhanced;
            let residual = *enhanced - observation;
            residual_energy += residual * residual;
        }
        let denominator = (raw_energy * enhanced_energy).sqrt();
        let correlation = if denominator > 0.0 {
            cross_energy / denominator
        } else {
            0.0
        };
        let residual_ratio = residual_energy / raw_energy.max(f64::EPSILON);
        if !correlation.is_finite()
            || !residual_ratio.is_finite()
            || correlation < OBSERVATION_MINIMUM_CORRELATION
            || residual_ratio > OBSERVATION_MAXIMUM_RESIDUAL_RATIO
        {
            raw.zeroize();
            filtered.zeroize();
            self.quiet_spectral.reset();
            return QuietCompensationResult::Unchanged;
        }
        let phase_integrity = verify_quiet_phase_integrity(&raw, &filtered);
        if phase_integrity != QuietPhaseIntegrity::Consistent {
            raw.zeroize();
            filtered.zeroize();
            self.quiet_spectral.reset();
            self.quiet_spectral.phase_integrity = phase_integrity;
            return QuietCompensationResult::Unchanged;
        }
        let correlation_risk = ((1.0 - correlation) / 0.65).clamp(0.0, 1.0);
        let residual_risk = (residual_ratio / 9.0).clamp(0.0, 1.0);
        let requested_observation_mix = (OBSERVATION_MINIMUM_MIX
            + (OBSERVATION_MAXIMUM_MIX - OBSERVATION_MINIMUM_MIX)
                * correlation_risk.max(residual_risk))
        .clamp(OBSERVATION_MINIMUM_MIX, OBSERVATION_MAXIMUM_MIX);
        let observation_mix = (self.quiet_spectral.observation_mix * 0.35
            + requested_observation_mix * 0.65)
            .clamp(OBSERVATION_MINIMUM_MIX, OBSERVATION_MAXIMUM_MIX);

        let mut mixed_peak = 0.0_f64;
        for (observation, enhanced) in raw.iter().zip(filtered.iter_mut()) {
            *enhanced = observation_mix * observation + (1.0 - observation_mix) * *enhanced;
            mixed_peak = mixed_peak.max(enhanced.abs());
        }
        if !mixed_peak.is_finite() || mixed_peak == 0.0 {
            raw.zeroize();
            filtered.zeroize();
            self.quiet_spectral.reset();
            return QuietCompensationResult::Unchanged;
        }
        let headroom = (QUIET_PEAK_CEILING / mixed_peak).min(1.0);
        for (sample, output) in filtered.iter().zip(pcm.chunks_exact_mut(2)) {
            let scaled = (sample * headroom * 32_768.0)
                .round()
                .clamp(f64::from(i16::MIN), f64::from(i16::MAX)) as i16;
            output.copy_from_slice(&scaled.to_le_bytes());
        }
        raw.zeroize();
        filtered.zeroize();
        self.quiet_spectral.gain = applied_gain * headroom;
        self.quiet_spectral.observation_mix = observation_mix;
        self.quiet_spectral.previous_input = previous_input;
        self.quiet_spectral.previous_dc = previous_dc;
        self.quiet_spectral.phase_integrity = QuietPhaseIntegrity::Consistent;
        QuietCompensationResult::Compensated
    }
}

fn verify_quiet_phase_integrity(
    raw: &[f64; FRAME_BYTES / 2],
    enhanced: &[f64; FRAME_BYTES / 2],
) -> QuietPhaseIntegrity {
    if raw
        .iter()
        .chain(enhanced.iter())
        .any(|sample| !sample.is_finite())
    {
        return QuietPhaseIntegrity::Invalid;
    }
    let raw_peak = raw
        .iter()
        .fold(0.0_f64, |peak, sample| peak.max(sample.abs()));
    let enhanced_peak = enhanced
        .iter()
        .fold(0.0_f64, |peak, sample| peak.max(sample.abs()));
    if raw_peak == 0.0 || enhanced_peak == 0.0 {
        return QuietPhaseIntegrity::InsufficientSupport;
    }
    let raw_onset = support_start(raw, raw_peak * 0.20);
    let enhanced_onset = support_start(enhanced, enhanced_peak * 0.20);
    let (Some(raw_onset), Some(enhanced_onset)) = (raw_onset, enhanced_onset) else {
        return QuietPhaseIntegrity::InsufficientSupport;
    };
    if enhanced_onset.saturating_add(PHASE_MAXIMUM_ONSET_ADVANCE_SAMPLES) < raw_onset {
        return QuietPhaseIntegrity::NonCausalOnset;
    }
    let raw_end = support_end(raw, raw_peak * 0.12).unwrap_or(raw_onset);
    let enhanced_end = support_end(enhanced, enhanced_peak * 0.12).unwrap_or(enhanced_onset);
    if enhanced_end > raw_end.saturating_add(PHASE_MAXIMUM_SMEAR_SAMPLES) {
        return QuietPhaseIntegrity::TemporalSmear;
    }
    let Some(alignment_lag) = best_alignment_lag(raw, enhanced, 16) else {
        return QuietPhaseIntegrity::InsufficientSupport;
    };
    if alignment_lag.unsigned_abs() as f64 > PHASE_MAXIMUM_GROUP_DELAY_SAMPLES {
        return QuietPhaseIntegrity::ExcessGroupDelay;
    }

    let mut supported = 0_usize;
    let mut previous_phase: Option<(f64, f64)> = None;
    for frequency in PHASE_FREQUENCIES_HZ {
        let (raw_real, raw_imaginary) = complex_projection(raw, frequency);
        let (enhanced_real, enhanced_imaginary) = complex_projection(enhanced, frequency);
        let raw_energy = raw_real * raw_real + raw_imaginary * raw_imaginary;
        let enhanced_energy =
            enhanced_real * enhanced_real + enhanced_imaginary * enhanced_imaginary;
        if raw_energy < 1.0e-8 || enhanced_energy < 1.0e-8 {
            continue;
        }
        let cross_real = enhanced_real * raw_real + enhanced_imaginary * raw_imaginary;
        let cross_imaginary = enhanced_imaginary * raw_real - enhanced_real * raw_imaginary;
        let phase = cross_imaginary.atan2(cross_real);
        if !phase.is_finite() {
            return QuietPhaseIntegrity::Invalid;
        }
        if let Some((previous_frequency, previous)) = previous_phase {
            let phase_delta = wrap_phase(phase - previous);
            let radians_per_sample =
                core::f64::consts::TAU * (frequency - previous_frequency) / PHASE_SAMPLE_RATE_HZ;
            let group_delay_samples = -phase_delta / radians_per_sample;
            if !group_delay_samples.is_finite()
                || group_delay_samples.abs() > PHASE_MAXIMUM_GROUP_DELAY_SAMPLES
            {
                return QuietPhaseIntegrity::ExcessGroupDelay;
            }
        }
        previous_phase = Some((frequency, phase));
        supported = supported.saturating_add(1);
    }
    if supported < PHASE_MINIMUM_SUPPORTED_BANDS {
        QuietPhaseIntegrity::InsufficientSupport
    } else {
        QuietPhaseIntegrity::Consistent
    }
}

fn support_start(samples: &[f64], threshold: f64) -> Option<usize> {
    samples.iter().position(|sample| sample.abs() >= threshold)
}

fn support_end(samples: &[f64], threshold: f64) -> Option<usize> {
    samples.iter().rposition(|sample| sample.abs() >= threshold)
}

fn best_alignment_lag(raw: &[f64], enhanced: &[f64], maximum_lag: isize) -> Option<isize> {
    let mut best: Option<(f64, isize)> = None;
    for lag in -maximum_lag..=maximum_lag {
        let mut cross = 0.0_f64;
        let mut raw_energy = 0.0_f64;
        let mut enhanced_energy = 0.0_f64;
        for (raw_index, observation) in raw.iter().copied().enumerate() {
            let enhanced_index = raw_index as isize + lag;
            if !(0..enhanced.len() as isize).contains(&enhanced_index) {
                continue;
            }
            let candidate = enhanced[enhanced_index as usize];
            cross += observation * candidate;
            raw_energy += observation * observation;
            enhanced_energy += candidate * candidate;
        }
        let denominator = (raw_energy * enhanced_energy).sqrt();
        if denominator <= f64::EPSILON {
            continue;
        }
        let correlation = cross / denominator;
        if correlation.is_finite()
            && best.is_none_or(|(best_correlation, _)| correlation > best_correlation)
        {
            best = Some((correlation, lag));
        }
    }
    best.map(|(_, lag)| lag)
}

fn complex_projection(samples: &[f64], frequency_hz: f64) -> (f64, f64) {
    let last = samples.len().saturating_sub(1).max(1) as f64;
    samples
        .iter()
        .enumerate()
        .fold((0.0_f64, 0.0_f64), |(real, imaginary), (index, sample)| {
            let window = 0.5 - 0.5 * (core::f64::consts::TAU * index as f64 / last).cos();
            let phase = core::f64::consts::TAU * frequency_hz * index as f64 / PHASE_SAMPLE_RATE_HZ;
            (
                real + sample * window * phase.cos(),
                imaginary - sample * window * phase.sin(),
            )
        })
}

fn wrap_phase(mut phase: f64) -> f64 {
    while phase > core::f64::consts::PI {
        phase -= core::f64::consts::TAU;
    }
    while phase < -core::f64::consts::PI {
        phase += core::f64::consts::TAU;
    }
    phase
}

impl Drop for PcmRing {
    fn drop(&mut self) {
        self.wipe_all();
    }
}

#[cfg(any(test, target_arch = "wasm32"))]
fn parse_context_frame(value: f64) -> Option<u64> {
    if !value.is_finite()
        || !(0.0..=MAXIMUM_SAFE_JS_INTEGER).contains(&value)
        || value.fract() != 0.0
    {
        return None;
    }
    Some(value as u64)
}

#[cfg(target_arch = "wasm32")]
mod wasm_boundary {
    use js_sys::Uint8Array;
    use wasm_bindgen::prelude::*;
    use zeroize::Zeroize;

    use super::{
        FRAME_BYTES, OBSERVATION_MAXIMUM_MIX, OBSERVATION_MINIMUM_MIX, OverflowPolicy, PcmRing,
        PushResult, QuietCompensationResult, QuietPhaseIntegrity, TURN_REFERENCE_MAXIMUM_FRAMES,
        TurnReferencePhase, TurnReferenceWindow, parse_context_frame, verify_quiet_phase_integrity,
    };

    /// Browser release fixture for the same audio-core clock transition used
    /// by the production client Wasm. It carries timing metadata only.
    #[wasm_bindgen(js_name = temporalVadClockSelfTest)]
    pub fn temporal_vad_clock_self_test() -> bool {
        let Ok(tick) = kotae_audio_core::advance_temporal_vad_clock(48_000, 1_000, 1_480, 6_280)
        else {
            return false;
        };
        tick.credited_ms == 40.0
            && tick.elapsed_ms == 110.0
            && kotae_audio_core::advance_temporal_vad_clock(48_000, 1_000, 6_280, 6_280).is_err()
            && kotae_audio_core::advance_temporal_vad_clock(48_000, 1_000, 6_280, 6_279).is_err()
    }

    #[wasm_bindgen(js_name = intentionalFastLaneSelfTest)]
    pub fn intentional_fast_lane_self_test() -> bool {
        use kotae_audio_core::{
            INTERRUPT_FRAME_FOREGROUND_VOICED, INTERRUPT_FRAME_GUARD_VOICED,
            INTERRUPT_FRAME_VOICED, IntentionalInterruptPhase, IntentionalInterruptState,
            advance_intentional_interrupt,
        };
        let foreground = INTERRUPT_FRAME_GUARD_VOICED
            | INTERRUPT_FRAME_VOICED
            | INTERRUPT_FRAME_FOREGROUND_VOICED;
        let mut intentional = IntentionalInterruptState::default();
        let mut decided_at = None;
        for frame in 1..=13_u16 {
            let (rms, peak) = if frame % 2 == 0 {
                (0.08, 0.21)
            } else {
                (0.045, 0.12)
            };
            let Ok(step) = advance_intentional_interrupt(
                intentional,
                foreground,
                rms,
                peak,
                40,
                frame * 40,
                true,
            ) else {
                return false;
            };
            intentional = step.state;
            if step.fast_ready {
                decided_at.get_or_insert(frame * 40);
            }
        }
        let mut constant = IntentionalInterruptState::default();
        let mut constant_ready = false;
        for frame in 1..=17_u16 {
            let Ok(step) = advance_intentional_interrupt(
                constant,
                foreground,
                0.06,
                0.16,
                40,
                frame * 40,
                true,
            ) else {
                return false;
            };
            constant = step.state;
            constant_ready |= step.fast_ready;
        }
        let Ok(lost) = advance_intentional_interrupt(
            IntentionalInterruptState::default(),
            foreground,
            0.08,
            0.21,
            40,
            40,
            false,
        ) else {
            return false;
        };
        decided_at == Some(400)
            && !constant_ready
            && lost.state.phase == IntentionalInterruptPhase::LegacyOnly
            && !lost.fast_ready
    }

    #[wasm_bindgen(js_name = intentionalFastLaneFrameSelfTest)]
    pub fn intentional_fast_lane_frame_self_test() -> bool {
        use kotae_audio_core::{
            INTERRUPT_FRAME_FOREGROUND_VOICED, INTERRUPT_FRAME_GUARD_VOICED,
            INTERRUPT_FRAME_VOICED, IntentionalInterruptPhase, IntentionalInterruptState,
            advance_intentional_interrupt,
        };
        let state = IntentionalInterruptState {
            phase: IntentionalInterruptPhase::FastReady,
            score: 32,
            foreground_ms: 360,
            change_count: 8,
            gap_ms: 0,
            last_bucket: 2,
            last_elapsed_ms: 360,
        };
        let flags = INTERRUPT_FRAME_GUARD_VOICED
            | INTERRUPT_FRAME_VOICED
            | INTERRUPT_FRAME_FOREGROUND_VOICED;
        let Ok(step) = advance_intentional_interrupt(state, flags, 0.08, 0.21, 40, 400, true)
        else {
            return false;
        };
        step.fast_ready
            && step.state.phase == IntentionalInterruptPhase::Confirmed
            && step.state.last_elapsed_ms == 400
    }

    #[wasm_bindgen(js_name = observationAddingSelfTest)]
    pub fn observation_adding_self_test() -> bool {
        use core::f64::consts::TAU;

        let mut input = [0_u8; FRAME_BYTES];
        for (index, bytes) in input.chunks_exact_mut(2).enumerate() {
            let time = index as f64 / 16_000.0;
            let sample = 0.009 * (TAU * 250.0 * time).sin()
                + 0.007 * (TAU * 1_800.0 * time).sin()
                + 0.004 * (TAU * 4_000.0 * time).sin();
            let pcm = (sample * 32_768.0)
                .round()
                .clamp(f64::from(i16::MIN), f64::from(i16::MAX)) as i16;
            bytes.copy_from_slice(&pcm.to_le_bytes());
        }
        let mut original = input;
        let Ok(mut ring) = PcmRing::new(91, 2, OverflowPolicy::Reject) else {
            input.zeroize();
            return false;
        };
        let result = ring.compensate_quiet_frame(91, &mut input);
        let mix = ring.quiet_spectral.observation_mix;
        let mut raw_onset = [0.0_f64; FRAME_BYTES / 2];
        let mut advanced = [0.0_f64; FRAME_BYTES / 2];
        for index in 32..160 {
            raw_onset[index] = 0.01 * (TAU * 1_000.0 * index as f64 / 16_000.0).sin();
            advanced[index - 4] = raw_onset[index];
        }
        let non_causal_rejected = verify_quiet_phase_integrity(&raw_onset, &advanced)
            == QuietPhaseIntegrity::NonCausalOnset;
        let preserved = input != original
            && result == QuietCompensationResult::Compensated
            && ring.quiet_phase_integrity(91) == Some(QuietPhaseIntegrity::Consistent)
            && non_causal_rejected
            && (OBSERVATION_MINIMUM_MIX..=OBSERVATION_MAXIMUM_MIX).contains(&mix)
            && input
                .chunks_exact(2)
                .all(|bytes| i16::from_le_bytes([bytes[0], bytes[1]]).unsigned_abs() <= 26_870);
        input.zeroize();
        original.zeroize();
        raw_onset.zeroize();
        advanced.zeroize();
        preserved
    }

    #[wasm_bindgen(js_name = turnReferenceBoundarySelfTest)]
    pub fn turn_reference_boundary_self_test() -> bool {
        let generation = 117_u64;
        let mut pcm = [0_u8; FRAME_BYTES];
        for (index, bytes) in pcm.chunks_exact_mut(2).enumerate() {
            let sample = 0.006 * (core::f64::consts::TAU * 1_000.0 * index as f64 / 16_000.0).sin();
            bytes.copy_from_slice(&((sample * 32_768.0) as i16).to_le_bytes());
        }
        let valid = (|| {
            let mut reference = TurnReferenceWindow::new(generation, 48_000).ok()?;
            for frame in 1..=TURN_REFERENCE_MAXIMUM_FRAMES {
                let phase = reference.advance(
                    generation,
                    frame as u64 * 960,
                    &pcm,
                    true,
                    true,
                    false,
                    false,
                );
                if frame < TURN_REFERENCE_MAXIMUM_FRAMES && phase != TurnReferencePhase::Collecting
                {
                    return None;
                }
            }
            let ready = reference.phase(generation) == TurnReferencePhase::Ready
                && reference.count(generation) == Some(TURN_REFERENCE_MAXIMUM_FRAMES);
            let stale = reference.advance(generation + 1, 21 * 960, &pcm, true, true, false, false);
            Some(
                ready
                    && stale == TurnReferencePhase::Unresolved
                    && reference.count(generation) == Some(0),
            )
        })()
        .unwrap_or(false);
        pcm.zeroize();
        valid
    }

    #[wasm_bindgen(js_name = TurnReferenceWindow)]
    pub struct WasmTurnReferenceWindow {
        inner: Option<TurnReferenceWindow>,
    }

    #[wasm_bindgen(js_class = TurnReferenceWindow)]
    impl WasmTurnReferenceWindow {
        #[wasm_bindgen(constructor)]
        pub fn new(generation: f64, context_sample_rate_hz: u32) -> WasmTurnReferenceWindow {
            Self {
                inner: parse_context_frame(generation)
                    .filter(|generation| *generation > 0)
                    .and_then(|generation| {
                        TurnReferenceWindow::new(generation, context_sample_rate_hz).ok()
                    }),
            }
        }

        pub fn phase(&self, generation: f64) -> i8 {
            let Some(generation) = parse_context_frame(generation) else {
                return -1;
            };
            self.inner
                .as_ref()
                .map_or(-1, |inner| inner.phase(generation) as i8)
        }

        pub fn count(&self, generation: f64) -> i8 {
            let Some(generation) = parse_context_frame(generation) else {
                return -1;
            };
            self.inner
                .as_ref()
                .and_then(|inner| inner.count(generation))
                .map_or(-1, |count| count as i8)
        }

        #[allow(clippy::too_many_arguments)]
        pub fn advance(
            &mut self,
            generation: f64,
            context_frame: f64,
            pcm: &Uint8Array,
            quiet_confirmed: bool,
            aec_verified: bool,
            output_active: bool,
            overlap: bool,
        ) -> i8 {
            let Some(inner) = self.inner.as_mut() else {
                return -1;
            };
            let Some(generation) = parse_context_frame(generation) else {
                return -1;
            };
            let Some(context_frame) = parse_context_frame(context_frame) else {
                return -1;
            };
            if pcm.length() as usize != FRAME_BYTES {
                return inner.advance(
                    generation,
                    context_frame,
                    &[],
                    quiet_confirmed,
                    aec_verified,
                    output_active,
                    overlap,
                ) as i8;
            }
            let mut frame = [0_u8; FRAME_BYTES];
            pcm.copy_to(&mut frame);
            let phase = inner.advance(
                generation,
                context_frame,
                &frame,
                quiet_confirmed,
                aec_verified,
                output_active,
                overlap,
            ) as i8;
            frame.zeroize();
            phase
        }

        pub fn clear(&mut self, generation: f64) -> bool {
            let Some(generation) = parse_context_frame(generation) else {
                return false;
            };
            self.inner
                .as_mut()
                .is_some_and(|inner| inner.clear(generation))
        }
    }

    #[wasm_bindgen(js_name = PcmRing)]
    pub struct WasmPcmRing {
        inner: Option<PcmRing>,
    }

    #[wasm_bindgen(js_class = PcmRing)]
    impl WasmPcmRing {
        #[wasm_bindgen(constructor)]
        pub fn new(generation: f64, capacity: u32, overwrite_oldest: bool) -> WasmPcmRing {
            let policy = if overwrite_oldest {
                OverflowPolicy::OverwriteOldest
            } else {
                OverflowPolicy::Reject
            };
            Self {
                inner: parse_context_frame(generation)
                    .filter(|generation| *generation > 0)
                    .and_then(|generation| {
                        PcmRing::new(generation, capacity as usize, policy).ok()
                    }),
            }
        }

        pub fn generation(&self) -> f64 {
            self.inner
                .as_ref()
                .map_or(-1.0, |inner| inner.generation() as f64)
        }

        pub fn capacity(&self) -> u32 {
            self.inner
                .as_ref()
                .map_or(0, |inner| inner.capacity() as u32)
        }

        pub fn count(&self, generation: f64) -> i32 {
            let Some(generation) = parse_context_frame(generation) else {
                return -1;
            };
            self.inner
                .as_ref()
                .and_then(|inner| inner.count(generation))
                .map_or(-1, |count| count as i32)
        }

        pub fn push(&mut self, generation: f64, context_frame: f64, pcm: &Uint8Array) -> u8 {
            let Some(inner) = self.inner.as_mut() else {
                return PushResult::Invalid as u8;
            };
            let Some(generation) = parse_context_frame(generation) else {
                return PushResult::Invalid as u8;
            };
            let Some(context_frame) = parse_context_frame(context_frame) else {
                return PushResult::Invalid as u8;
            };
            if pcm.length() as usize != FRAME_BYTES {
                return PushResult::Invalid as u8;
            }
            let mut frame = [0_u8; FRAME_BYTES];
            pcm.copy_to(&mut frame);
            let result = inner.push(generation, context_frame, &frame) as u8;
            frame.zeroize();
            result
        }

        #[wasm_bindgen(js_name = compensateQuietFrame)]
        pub fn compensate_quiet_frame(&mut self, generation: f64, pcm: &Uint8Array) -> u8 {
            let Some(inner) = self.inner.as_mut() else {
                return QuietCompensationResult::Invalid as u8;
            };
            let Some(generation) = parse_context_frame(generation) else {
                return QuietCompensationResult::Invalid as u8;
            };
            if pcm.length() as usize != FRAME_BYTES {
                return QuietCompensationResult::Invalid as u8;
            }
            let mut frame = [0_u8; FRAME_BYTES];
            pcm.copy_to(&mut frame);
            let result = inner.compensate_quiet_frame(generation, &mut frame);
            if result == QuietCompensationResult::Compensated {
                pcm.copy_from(&frame);
            }
            frame.zeroize();
            result as u8
        }

        #[wasm_bindgen(js_name = quietPhaseIntegrity)]
        pub fn quiet_phase_integrity(&self, generation: f64) -> i8 {
            let Some(generation) = parse_context_frame(generation) else {
                return -1;
            };
            self.inner
                .as_ref()
                .and_then(|inner| inner.quiet_phase_integrity(generation))
                .map_or(-1, |integrity| integrity as i8)
        }

        #[wasm_bindgen(js_name = deriveWeakFrame)]
        pub fn derive_weak_frame(
            &mut self,
            generation: f64,
            context_frame: f64,
            baseline: &Uint8Array,
            enhanced: &Uint8Array,
            destination: &Uint8Array,
        ) -> bool {
            let Some(inner) = self.inner.as_mut() else {
                return false;
            };
            let (Some(generation), Some(context_frame)) = (
                parse_context_frame(generation),
                parse_context_frame(context_frame),
            ) else {
                return false;
            };
            if baseline.length() as usize != FRAME_BYTES
                || enhanced.length() as usize != FRAME_BYTES
                || destination.length() as usize != FRAME_BYTES
            {
                return false;
            }
            let mut raw = [0_u8; FRAME_BYTES];
            let mut strong = [0_u8; FRAME_BYTES];
            let mut weak = [0_u8; FRAME_BYTES];
            baseline.copy_to(&mut raw);
            enhanced.copy_to(&mut strong);
            let result =
                inner.derive_weak_frame(generation, context_frame, &raw, &strong, &mut weak);
            if result {
                destination.copy_from(&weak);
            }
            raw.zeroize();
            strong.zeroize();
            weak.zeroize();
            result
        }

        #[wasm_bindgen(js_name = shiftInto)]
        pub fn shift_into(&mut self, generation: f64, destination: &Uint8Array) -> f64 {
            let Some(inner) = self.inner.as_mut() else {
                return -2.0;
            };
            let Some(generation) = parse_context_frame(generation) else {
                return -2.0;
            };
            if destination.length() as usize != FRAME_BYTES {
                return -2.0;
            }
            let Some(count) = inner.count(generation) else {
                return -2.0;
            };
            if count == 0 {
                return -1.0;
            }
            let mut pcm = [0_u8; FRAME_BYTES];
            let Some(context_frame) = inner.shift_into(generation, &mut pcm) else {
                pcm.zeroize();
                return -2.0;
            };
            destination.copy_from(&pcm);
            pcm.zeroize();
            context_frame as f64
        }

        pub fn clear(&mut self, generation: f64) -> bool {
            let Some(generation) = parse_context_frame(generation) else {
                return false;
            };
            self.inner
                .as_mut()
                .is_some_and(|inner| inner.clear(generation))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::f64::consts::TAU;

    fn frame(value: u8) -> [u8; FRAME_BYTES] {
        [value; FRAME_BYTES]
    }

    fn tonal_frame(amplitude: f64) -> [u8; FRAME_BYTES] {
        let mut output = [0_u8; FRAME_BYTES];
        for (index, bytes) in output.chunks_exact_mut(2).enumerate() {
            let time = index as f64 / 16_000.0;
            let sample = amplitude * ((TAU * 250.0 * time).sin() + (TAU * 4_000.0 * time).sin());
            let pcm = (sample * 32_768.0)
                .round()
                .clamp(f64::from(i16::MIN), f64::from(i16::MAX)) as i16;
            bytes.copy_from_slice(&pcm.to_le_bytes());
        }
        output
    }

    fn projection(frame: &[u8; FRAME_BYTES], frequency: f64) -> f64 {
        frame
            .chunks_exact(2)
            .enumerate()
            .map(|(index, bytes)| {
                let sample = f64::from(i16::from_le_bytes([bytes[0], bytes[1]])) / 32_768.0;
                sample * (TAU * frequency * index as f64 / 16_000.0).sin()
            })
            .sum::<f64>()
            .abs()
    }

    #[test]
    fn turn_reference_becomes_ready_at_four_hundred_ms_only() {
        let generation = 71_u64;
        let mut reference = TurnReferenceWindow::new(generation, 48_000).expect("reference");
        let voice = tonal_frame(0.006);
        for frame in 1..TURN_REFERENCE_MAXIMUM_FRAMES {
            assert_eq!(
                reference.advance(
                    generation,
                    frame as u64 * 960,
                    &voice,
                    true,
                    true,
                    false,
                    false,
                ),
                TurnReferencePhase::Collecting
            );
        }
        assert_eq!(reference.count(generation), Some(19));
        assert_eq!(
            reference.advance(generation, 20 * 960, &voice, true, true, false, false,),
            TurnReferencePhase::Ready
        );
        assert_eq!(reference.count(generation), Some(20));

        // Ready is terminal: a twenty-first frame cannot extend the reference.
        assert_eq!(
            reference.advance(generation, 21 * 960, &voice, true, true, false, false,),
            TurnReferencePhase::Unresolved
        );
        assert_eq!(reference.count(generation), Some(0));
    }

    #[test]
    fn ambiguous_or_stale_turn_reference_zeroizes_all_support() {
        let voice = tonal_frame(0.006);
        for fault in 0..5 {
            let mut reference = TurnReferenceWindow::new(81, 48_000).expect("reference");
            assert_eq!(
                reference.advance(81, 960, &voice, true, true, false, false),
                TurnReferencePhase::Collecting
            );
            let phase = match fault {
                0 => reference.advance(82, 1_920, &voice, true, true, false, false),
                1 => reference.advance(81, 1_920, &voice, true, false, false, false),
                2 => reference.advance(81, 1_920, &voice, true, true, true, false),
                3 => reference.advance(81, 1_920, &voice, true, true, false, true),
                _ => reference.advance(81, 960, &voice, true, true, false, false),
            };
            assert_eq!(phase, TurnReferencePhase::Unresolved);
            assert_eq!(reference.count(81), Some(0));
        }
    }

    #[test]
    fn unresolved_h0_never_reaches_ready_in_one_hundred_thousand_windows() {
        let voice = tonal_frame(0.006);
        let mut false_ready = 0_u32;
        for generation in 1..=100_000_u64 {
            let mut reference = TurnReferenceWindow::new(generation, 48_000).expect("reference");
            let phase = reference.advance(generation, 960, &voice, true, false, false, false);
            false_ready += u32::from(phase == TurnReferencePhase::Ready);
            assert_eq!(reference.count(generation), Some(0));
        }
        assert_eq!(false_ready, 0);
    }

    #[test]
    fn capacity_is_finite_and_nonzero() {
        assert!(PcmRing::new(1, 0, OverflowPolicy::Reject).is_err());
        assert!(PcmRing::new(1, MAXIMUM_CAPACITY + 1, OverflowPolicy::Reject).is_err());
        assert!(PcmRing::new(1, MAXIMUM_CAPACITY, OverflowPolicy::Reject).is_ok());
        assert!(PcmRing::new(0, 1, OverflowPolicy::Reject).is_err());
    }

    #[test]
    fn overwrite_ring_keeps_only_newest_frames_in_fifo_order() {
        let mut ring = PcmRing::new(7, 2, OverflowPolicy::OverwriteOldest).unwrap();
        assert_eq!(ring.push(7, 10, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(7, 20, &frame(2)), PushResult::Inserted);
        assert_eq!(
            ring.push(7, 30, &frame(3)),
            PushResult::InsertedAfterEviction
        );
        assert_eq!(ring.count(7), Some(2));

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(7, &mut output), Some(20));
        assert_eq!(output, frame(2));
        assert_eq!(ring.shift_into(7, &mut output), Some(30));
        assert_eq!(output, frame(3));
        assert_eq!(ring.shift_into(7, &mut output), None);
    }

    #[test]
    fn reject_ring_never_changes_when_full() {
        let mut ring = PcmRing::new(8, 1, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(8, 10, &frame(4)), PushResult::Inserted);
        assert_eq!(ring.push(8, 20, &frame(9)), PushResult::Full);
        assert_eq!(ring.count(8), Some(1));

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(8, &mut output), Some(10));
        assert_eq!(output, frame(4));
    }

    #[test]
    fn invalid_frames_are_never_retained() {
        let mut ring = PcmRing::new(9, 2, OverflowPolicy::Reject).unwrap();
        assert_eq!(
            ring.push(9, 1, &[7_u8; FRAME_BYTES - 1]),
            PushResult::Invalid
        );
        assert_eq!(ring.count(9), Some(0));
        assert_eq!(ring.shift_into(9, &mut [0_u8; FRAME_BYTES - 1]), None);
    }

    #[test]
    fn duplicate_or_backward_context_frames_fail_without_mutation() {
        let mut ring = PcmRing::new(10, 3, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(10, 20, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(10, 20, &frame(2)), PushResult::Invalid);
        assert_eq!(ring.push(10, 19, &frame(3)), PushResult::Invalid);
        assert_eq!(ring.count(10), Some(1));

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(10, &mut output), Some(20));
        assert_eq!(output, frame(1));
    }

    #[test]
    fn clear_wipes_entries_and_resets_fifo() {
        let mut ring = PcmRing::new(11, 2, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(11, 1, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(11, 2, &frame(2)), PushResult::Inserted);
        assert!(ring.clear(11));
        assert_eq!(ring.count(11), Some(0));
        assert_eq!(ring.head, 0);
        assert!(ring.slots.iter().all(|slot| !slot.occupied));
        assert!(
            ring.slots
                .iter()
                .all(|slot| slot.pcm.iter().all(|byte| *byte == 0))
        );
    }

    #[test]
    fn context_frame_parser_accepts_only_exact_safe_integers() {
        assert_eq!(parse_context_frame(0.0), Some(0));
        assert_eq!(
            parse_context_frame(MAXIMUM_SAFE_JS_INTEGER),
            Some(9_007_199_254_740_991)
        );
        for invalid in [
            f64::NAN,
            f64::INFINITY,
            -1.0,
            1.5,
            MAXIMUM_SAFE_JS_INTEGER + 1.0,
        ] {
            assert_eq!(parse_context_frame(invalid), None);
        }
    }

    #[test]
    fn entry_wipe_clears_every_owned_byte() {
        let mut entry = Slot {
            occupied: true,
            context_frame: 1,
            pcm: frame(0xa5),
        };
        entry.wipe();
        assert!(!entry.occupied);
        assert_eq!(entry.context_frame, 0);
        assert!(entry.pcm.iter().all(|byte| *byte == 0));
    }

    #[test]
    fn stale_generation_cannot_read_mutate_or_clear_the_owner_ring() {
        let mut ring = PcmRing::new(41, 2, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.generation(), 41);
        assert_eq!(ring.push(41, 10, &frame(0xa5)), PushResult::Inserted);

        assert_eq!(ring.count(42), None);
        assert_eq!(ring.push(42, 20, &frame(0x5a)), PushResult::Invalid);
        assert!(!ring.clear(42));
        let mut stale_output = frame(0x3c);
        assert_eq!(ring.shift_into(42, &mut stale_output), None);
        assert_eq!(stale_output, frame(0x3c));

        assert_eq!(ring.count(41), Some(1));
        let mut owner_output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(41, &mut owner_output), Some(10));
        assert_eq!(owner_output, frame(0xa5));
    }

    #[test]
    fn weak_view_is_generation_and_sample_clock_bound() {
        let mut ring = PcmRing::new(45, 2, OverflowPolicy::Reject).unwrap();
        let baseline = [0_u8; FRAME_BYTES];
        let enhanced = frame(4);
        let mut weak = frame(0x3c);
        assert!(!ring.derive_weak_frame(46, 100, &baseline, &enhanced, &mut weak));
        assert_eq!(weak, frame(0x3c));
        assert!(ring.derive_weak_frame(45, 100, &baseline, &enhanced, &mut weak));
        assert_eq!(&weak[..4], &[1, 1, 1, 1]);

        let unchanged = weak;
        assert!(!ring.derive_weak_frame(45, 100, &baseline, &enhanced, &mut weak));
        assert_eq!(weak, unchanged);
        assert!(!ring.derive_weak_frame(45, 99, &baseline, &enhanced, &mut weak));
        assert_eq!(weak, unchanged);
        assert!(ring.clear(45));
        assert!(ring.derive_weak_frame(45, 99, &baseline, &enhanced, &mut weak));
    }

    #[test]
    fn quiet_compensation_is_generation_bound_and_spectrally_tilted() {
        let mut ring = PcmRing::new(51, 2, OverflowPolicy::Reject).unwrap();
        let original = tonal_frame(0.012);
        let mut stale = original;
        assert_eq!(
            ring.compensate_quiet_frame(52, &mut stale),
            QuietCompensationResult::Invalid
        );
        assert_eq!(stale, original);

        let input_ratio = projection(&original, 4_000.0) / projection(&original, 250.0);
        let mut compensated = original;
        assert_eq!(
            ring.compensate_quiet_frame(51, &mut compensated),
            QuietCompensationResult::Compensated
        );
        assert_eq!(
            ring.quiet_phase_integrity(51),
            Some(QuietPhaseIntegrity::Consistent)
        );
        assert_eq!(ring.quiet_phase_integrity(52), None);
        let output_ratio = projection(&compensated, 4_000.0) / projection(&compensated, 250.0);
        assert!(output_ratio > input_ratio * 1.15);
        let output_peak = compensated
            .chunks_exact(2)
            .map(|bytes| i16::from_le_bytes([bytes[0], bytes[1]]).unsigned_abs())
            .max()
            .unwrap();
        assert!(output_peak <= (QUIET_PEAK_CEILING * 32_768.0) as u16);
    }

    #[test]
    fn silence_and_normal_voice_are_byte_stable() {
        let mut ring = PcmRing::new(61, 2, OverflowPolicy::Reject).unwrap();
        let mut silence = [0_u8; FRAME_BYTES];
        assert_eq!(
            ring.compensate_quiet_frame(61, &mut silence),
            QuietCompensationResult::Unchanged
        );
        assert_eq!(silence, [0_u8; FRAME_BYTES]);

        let original = tonal_frame(0.09);
        let mut normal = original;
        assert_eq!(
            ring.compensate_quiet_frame(61, &mut normal),
            QuietCompensationResult::Unchanged
        );
        assert_eq!(normal, original);
    }

    #[test]
    fn clear_zeroizes_quiet_spectral_history() {
        let mut ring = PcmRing::new(71, 2, OverflowPolicy::Reject).unwrap();
        let original = tonal_frame(0.012);
        let mut first = original;
        assert_eq!(
            ring.compensate_quiet_frame(71, &mut first),
            QuietCompensationResult::Compensated
        );
        assert!(ring.quiet_spectral.gain > 1.0);
        assert!(ring.clear(71));
        assert_eq!(ring.quiet_spectral.gain, 1.0);
        assert_eq!(ring.quiet_spectral.observation_mix, OBSERVATION_MAXIMUM_MIX);
        assert_eq!(ring.quiet_spectral.previous_input, 0.0);
        assert_eq!(ring.quiet_spectral.previous_dc, 0.0);
        assert_eq!(
            ring.quiet_spectral.phase_integrity,
            QuietPhaseIntegrity::Invalid
        );
    }

    #[test]
    fn observation_adding_preserves_raw_support_under_large_enhancement() {
        let mut ring = PcmRing::new(81, 2, OverflowPolicy::Reject).unwrap();
        let original = tonal_frame(0.009);
        let mut compensated = original;
        assert_eq!(
            ring.compensate_quiet_frame(81, &mut compensated),
            QuietCompensationResult::Compensated
        );
        assert!(
            (OBSERVATION_MINIMUM_MIX..=OBSERVATION_MAXIMUM_MIX)
                .contains(&ring.quiet_spectral.observation_mix)
        );
        assert!(projection(&compensated, 250.0) >= projection(&original, 250.0));
        assert!(projection(&compensated, 4_000.0) > projection(&original, 4_000.0));
    }

    #[test]
    fn excessive_distortion_returns_the_original_bytes() {
        let mut ring = PcmRing::new(82, 2, OverflowPolicy::Reject).unwrap();
        let mut impulse = [0_u8; FRAME_BYTES];
        impulse[0..2].copy_from_slice(&1_311_i16.to_le_bytes());
        let original = impulse;
        assert_eq!(
            ring.compensate_quiet_frame(82, &mut impulse),
            QuietCompensationResult::Unchanged
        );
        assert_eq!(impulse, original);
    }

    #[test]
    fn h0_bypass_is_byte_stable_for_one_hundred_thousand_frames() {
        let mut ring = PcmRing::new(83, 2, OverflowPolicy::Reject).unwrap();
        let silence = [0_u8; FRAME_BYTES];
        let normal = tonal_frame(0.09);
        for trace in 0..100_000_u32 {
            let original = if trace % 2 == 0 { silence } else { normal };
            let mut observed = original;
            assert_eq!(
                ring.compensate_quiet_frame(83, &mut observed),
                QuietCompensationResult::Unchanged
            );
            assert_eq!(observed, original);
        }
    }

    #[test]
    fn phase_integrity_rejects_one_hundred_thousand_advance_and_smear_counterexamples() {
        let mut rejected = 0_u32;
        for trace in 0..100_000_usize {
            let start = 32 + trace % 48;
            let end = start + 96;
            let mut raw = [0.0_f64; FRAME_BYTES / 2];
            for (index, sample) in raw.iter_mut().enumerate().take(end).skip(start) {
                *sample = 0.01
                    * (core::f64::consts::TAU * 1_000.0 * index as f64 / PHASE_SAMPLE_RATE_HZ)
                        .sin();
            }
            let mut counterexample = [0.0_f64; FRAME_BYTES / 2];
            if trace % 2 == 0 {
                let advance = 2 + trace % 12;
                counterexample[(start - advance)..(end - advance)]
                    .copy_from_slice(&raw[start..end]);
            } else {
                counterexample.copy_from_slice(&raw);
                let smear = 13 + trace % 24;
                for (index, sample) in counterexample
                    .iter_mut()
                    .enumerate()
                    .take((end + smear).min(FRAME_BYTES / 2))
                    .skip(end)
                {
                    *sample = 0.01
                        * (core::f64::consts::TAU * 1_000.0 * index as f64 / PHASE_SAMPLE_RATE_HZ)
                            .sin();
                }
            }
            let class = verify_quiet_phase_integrity(&raw, &counterexample);
            rejected += u32::from(class != QuietPhaseIntegrity::Consistent);
            raw.zeroize();
            counterexample.zeroize();
        }
        assert_eq!(rejected, 100_000);
    }

    #[test]
    fn phase_integrity_accepts_causal_identity_and_rejects_excess_group_delay() {
        let mut raw = [0.0_f64; FRAME_BYTES / 2];
        for (index, sample) in raw.iter_mut().enumerate() {
            *sample = 0.008
                * ((core::f64::consts::TAU * 500.0 * index as f64 / PHASE_SAMPLE_RATE_HZ).sin()
                    + (core::f64::consts::TAU * 1_800.0 * index as f64 / PHASE_SAMPLE_RATE_HZ)
                        .sin()
                    + (core::f64::consts::TAU * 2_800.0 * index as f64 / PHASE_SAMPLE_RATE_HZ)
                        .sin());
        }
        assert_eq!(
            verify_quiet_phase_integrity(&raw, &raw),
            QuietPhaseIntegrity::Consistent
        );
        let mut delayed = [0.0_f64; FRAME_BYTES / 2];
        delayed[10..].copy_from_slice(&raw[..FRAME_BYTES / 2 - 10]);
        assert_eq!(
            verify_quiet_phase_integrity(&raw, &delayed),
            QuietPhaseIntegrity::ExcessGroupDelay
        );
        raw.zeroize();
        delayed.zeroize();
    }
}
