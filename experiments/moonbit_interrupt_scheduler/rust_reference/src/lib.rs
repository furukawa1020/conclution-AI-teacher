use kotae_audio_core::{
    IntentionalInterruptPhase, IntentionalInterruptState, advance_intentional_interrupt,
};

const INVALID: i64 = -1;

#[unsafe(no_mangle)]
#[allow(clippy::too_many_arguments)]
pub extern "C" fn advance_intentional_interrupt_packed(
    packed_state: i64,
    frame_flags: u32,
    rms: f64,
    peak: f64,
    credited_ms: u32,
    candidate_elapsed_ms: u32,
    aec_verified: u32,
) -> i64 {
    let Ok(phase) = IntentionalInterruptPhase::try_from((packed_state & 7) as u8) else {
        return INVALID;
    };
    let state = IntentionalInterruptState {
        phase,
        score: (((packed_state >> 3) & 63) as i16) - 24,
        foreground_ms: ((packed_state >> 9) & 4095) as u16,
        change_count: ((packed_state >> 21) & 15) as u8,
        gap_ms: ((packed_state >> 25) & 127) as u8,
        last_bucket: ((packed_state >> 32) & 7) as u8,
        last_elapsed_ms: ((packed_state >> 35) & 4095) as u16,
    };
    let Ok(frame_flags) = u8::try_from(frame_flags) else {
        return INVALID;
    };
    let Ok(credited_ms) = u8::try_from(credited_ms) else {
        return INVALID;
    };
    let Ok(candidate_elapsed_ms) = u16::try_from(candidate_elapsed_ms) else {
        return INVALID;
    };
    let Ok(step) = advance_intentional_interrupt(
        state,
        frame_flags,
        rms,
        peak,
        credited_ms,
        candidate_elapsed_ms,
        aec_verified != 0,
    ) else {
        return INVALID;
    };
    i64::from(step.state.phase as u8)
        | (i64::from(step.state.score + 24) << 3)
        | (i64::from(step.state.foreground_ms) << 9)
        | (i64::from(step.state.change_count) << 21)
        | (i64::from(step.state.gap_ms) << 25)
        | (i64::from(step.state.last_bucket) << 32)
        | (i64::from(step.state.last_elapsed_ms) << 35)
        | (i64::from(step.signal as u8) << 47)
        | (i64::from(step.fast_ready) << 49)
}
