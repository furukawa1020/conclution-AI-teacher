//! A finite PCM16 frame ring for the browser AudioWorklet boundary.
//!
//! The ring owns every completed frame while it is awaiting confirmation or
//! transport credit. Entries are wiped before eviction, removal, clear, and
//! drop. It deliberately exports no inspection API for retained audio.

use zeroize::Zeroize;

pub const FRAME_BYTES: usize = 640;
pub const MAXIMUM_CAPACITY: usize = 200;
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
    slots: Vec<Slot>,
    head: usize,
    count: usize,
    last_context_frame: Option<u64>,
    overflow_policy: OverflowPolicy,
}

impl PcmRing {
    pub fn new(capacity: usize, overflow_policy: OverflowPolicy) -> Result<Self, &'static str> {
        if capacity == 0 || capacity > MAXIMUM_CAPACITY {
            return Err("invalid_capacity");
        }
        Ok(Self {
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

    pub fn count(&self) -> usize {
        self.count
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

    pub fn push(&mut self, context_frame: u64, pcm: &[u8]) -> PushResult {
        self.push_frame(context_frame, pcm)
    }

    pub fn shift_into(&mut self, destination: &mut [u8]) -> Option<u64> {
        if destination.len() != FRAME_BYTES || self.count == 0 {
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

    pub fn clear(&mut self) {
        self.slots.iter_mut().for_each(Slot::wipe);
        self.head = 0;
        self.count = 0;
        self.last_context_frame = None;
    }
}

impl Drop for PcmRing {
    fn drop(&mut self) {
        self.clear();
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

    #[wasm_bindgen(js_name = PcmRing)]
    pub struct WasmPcmRing {
        inner: Option<PcmRing>,
    }

    #[wasm_bindgen(js_class = PcmRing)]
    impl WasmPcmRing {
        #[wasm_bindgen(constructor)]
        pub fn new(capacity: u32, overwrite_oldest: bool) -> WasmPcmRing {
            let policy = if overwrite_oldest {
                OverflowPolicy::OverwriteOldest
            } else {
                OverflowPolicy::Reject
            };
            Self {
                inner: PcmRing::new(capacity as usize, policy).ok(),
            }
        }

        pub fn capacity(&self) -> u32 {
            self.inner
                .as_ref()
                .map_or(0, |inner| inner.capacity() as u32)
        }

        pub fn count(&self) -> u32 {
            self.inner.as_ref().map_or(0, |inner| inner.count() as u32)
        }

        pub fn push(&mut self, context_frame: f64, pcm: &Uint8Array) -> u8 {
            let Some(inner) = self.inner.as_mut() else {
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
            let result = inner.push_frame(context_frame, &frame) as u8;
            frame.zeroize();
            result
        }

        #[wasm_bindgen(js_name = shiftInto)]
        pub fn shift_into(&mut self, destination: &Uint8Array) -> f64 {
            let Some(inner) = self.inner.as_mut() else {
                return -2.0;
            };
            if destination.length() as usize != FRAME_BYTES {
                return -2.0;
            }
            if inner.count() == 0 {
                return -1.0;
            }
            let mut pcm = [0_u8; FRAME_BYTES];
            let Some(context_frame) = inner.shift_into(&mut pcm) else {
                pcm.zeroize();
                return -2.0;
            };
            destination.copy_from(&pcm);
            pcm.zeroize();
            context_frame as f64
        }

        pub fn clear(&mut self) {
            if let Some(inner) = self.inner.as_mut() {
                inner.clear();
            }
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
        assert!(PcmRing::new(0, OverflowPolicy::Reject).is_err());
        assert!(PcmRing::new(MAXIMUM_CAPACITY + 1, OverflowPolicy::Reject).is_err());
        assert!(PcmRing::new(MAXIMUM_CAPACITY, OverflowPolicy::Reject).is_ok());
    }

    #[test]
    fn overwrite_ring_keeps_only_newest_frames_in_fifo_order() {
        let mut ring = PcmRing::new(2, OverflowPolicy::OverwriteOldest).unwrap();
        assert_eq!(ring.push(10, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(20, &frame(2)), PushResult::Inserted);
        assert_eq!(ring.push(30, &frame(3)), PushResult::InsertedAfterEviction);
        assert_eq!(ring.count(), 2);

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(&mut output), Some(20));
        assert_eq!(output, frame(2));
        assert_eq!(ring.shift_into(&mut output), Some(30));
        assert_eq!(output, frame(3));
        assert_eq!(ring.shift_into(&mut output), None);
    }

    #[test]
    fn reject_ring_never_changes_when_full() {
        let mut ring = PcmRing::new(1, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(10, &frame(4)), PushResult::Inserted);
        assert_eq!(ring.push(20, &frame(9)), PushResult::Full);
        assert_eq!(ring.count(), 1);

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(&mut output), Some(10));
        assert_eq!(output, frame(4));
    }

    #[test]
    fn invalid_frames_are_never_retained() {
        let mut ring = PcmRing::new(2, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(1, &[7_u8; FRAME_BYTES - 1]), PushResult::Invalid);
        assert_eq!(ring.count(), 0);
        assert_eq!(ring.shift_into(&mut [0_u8; FRAME_BYTES - 1]), None);
    }

    #[test]
    fn duplicate_or_backward_context_frames_fail_without_mutation() {
        let mut ring = PcmRing::new(3, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(20, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(20, &frame(2)), PushResult::Invalid);
        assert_eq!(ring.push(19, &frame(3)), PushResult::Invalid);
        assert_eq!(ring.count(), 1);

        let mut output = [0_u8; FRAME_BYTES];
        assert_eq!(ring.shift_into(&mut output), Some(20));
        assert_eq!(output, frame(1));
    }

    #[test]
    fn clear_wipes_entries_and_resets_fifo() {
        let mut ring = PcmRing::new(2, OverflowPolicy::Reject).unwrap();
        assert_eq!(ring.push(1, &frame(1)), PushResult::Inserted);
        assert_eq!(ring.push(2, &frame(2)), PushResult::Inserted);
        ring.clear();
        assert_eq!(ring.count(), 0);
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
}
