//! A finite PCM16 frame ring for the browser AudioWorklet boundary.
//!
//! The ring owns every completed frame while it is awaiting confirmation or
//! transport credit. Entries are wiped before eviction, removal, clear, and
//! drop. It deliberately exports no inspection API for retained audio.

use zeroize::Zeroize;

pub const FRAME_BYTES: usize = 640;
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
    overflow_policy: OverflowPolicy,
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
            overflow_policy,
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
    }
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

    use super::{FRAME_BYTES, OverflowPolicy, PcmRing, PushResult, parse_context_frame};

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

    fn frame(value: u8) -> [u8; FRAME_BYTES] {
        [value; FRAME_BYTES]
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
}
