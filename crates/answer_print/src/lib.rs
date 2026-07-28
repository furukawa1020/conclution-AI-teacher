//! 「答頭線」— a compact timeline of answer structure.
//!
//! It compares when a speaker reaches a conclusion without storing voice,
//! speaker embeddings, or lexical content. It must never be used as an
//! authentication or identity signal.

use core::fmt;

const MAX_ANSWER_MS: u32 = 180_000;
const DEFAULT_BIN_MS: u32 = 100;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[repr(u8)]
pub enum MarkKind {
    Silence = 0,
    Preamble = 1,
    Conclusion = 2,
    Evidence = 3,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TimedMark {
    pub kind: MarkKind,
    pub duration_ms: u32,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AnswerPrint {
    marks: Vec<TimedMark>,
    total_ms: u32,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Comparison {
    /// Zero means structurally identical after time alignment.
    pub structural_distance: f32,
    /// Positive means the current answer reached its conclusion later.
    pub conclusion_latency_delta_ms: Option<i32>,
    /// Positive means the current answer used more pre-conclusion time.
    pub pre_conclusion_delta_ms: i32,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PrintError {
    Empty,
    ZeroDuration,
    TooLong,
    InvalidBinWidth,
}

impl fmt::Display for PrintError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Empty => formatter.write_str("an answer print requires at least one mark"),
            Self::ZeroDuration => formatter.write_str("mark duration must be greater than zero"),
            Self::TooLong => formatter.write_str("answer print exceeds the maximum duration"),
            Self::InvalidBinWidth => {
                formatter.write_str("bin width must be between 20ms and 1000ms")
            }
        }
    }
}

impl std::error::Error for PrintError {}

impl AnswerPrint {
    pub fn new(marks: impl IntoIterator<Item = TimedMark>) -> Result<Self, PrintError> {
        let mut normalized: Vec<TimedMark> = Vec::new();
        let mut total_ms = 0_u32;

        for mark in marks {
            if mark.duration_ms == 0 {
                return Err(PrintError::ZeroDuration);
            }
            total_ms = total_ms
                .checked_add(mark.duration_ms)
                .ok_or(PrintError::TooLong)?;
            if total_ms > MAX_ANSWER_MS {
                return Err(PrintError::TooLong);
            }

            if let Some(previous) = normalized.last_mut()
                && previous.kind == mark.kind
            {
                previous.duration_ms = previous
                    .duration_ms
                    .checked_add(mark.duration_ms)
                    .ok_or(PrintError::TooLong)?;
                continue;
            }
            normalized.push(mark);
        }

        if normalized.is_empty() {
            return Err(PrintError::Empty);
        }
        Ok(Self {
            marks: normalized,
            total_ms,
        })
    }

    pub fn marks(&self) -> &[TimedMark] {
        &self.marks
    }

    pub fn total_ms(&self) -> u32 {
        self.total_ms
    }

    pub fn conclusion_latency_ms(&self) -> Option<u32> {
        let mut elapsed = 0_u32;
        for mark in &self.marks {
            if mark.kind == MarkKind::Conclusion {
                return Some(elapsed);
            }
            elapsed = elapsed.saturating_add(mark.duration_ms);
        }
        None
    }

    pub fn pre_conclusion_ms(&self) -> u32 {
        self.conclusion_latency_ms().unwrap_or(self.total_ms)
    }

    pub fn compare(&self, baseline: &Self) -> Comparison {
        let current = self.resample(DEFAULT_BIN_MS).unwrap_or_default();
        let reference = baseline.resample(DEFAULT_BIN_MS).unwrap_or_default();
        Comparison {
            structural_distance: dynamic_time_warping(&current, &reference),
            conclusion_latency_delta_ms: signed_optional_delta(
                self.conclusion_latency_ms(),
                baseline.conclusion_latency_ms(),
            ),
            pre_conclusion_delta_ms: signed_delta(
                self.pre_conclusion_ms(),
                baseline.pre_conclusion_ms(),
            ),
        }
    }

    pub fn resample(&self, bin_ms: u32) -> Result<Vec<MarkKind>, PrintError> {
        if !(20..=1_000).contains(&bin_ms) {
            return Err(PrintError::InvalidBinWidth);
        }

        let bins = self.total_ms.div_ceil(bin_ms) as usize;
        let mut timeline = Vec::with_capacity(bins);
        let mut mark_index = 0_usize;
        let mut mark_ends_at = self.marks[0].duration_ms;

        for bin in 0..bins {
            let midpoint = (bin as u32)
                .saturating_mul(bin_ms)
                .saturating_add(bin_ms / 2)
                .min(self.total_ms.saturating_sub(1));
            while midpoint >= mark_ends_at && mark_index + 1 < self.marks.len() {
                mark_index += 1;
                mark_ends_at = mark_ends_at.saturating_add(self.marks[mark_index].duration_ms);
            }
            timeline.push(self.marks[mark_index].kind);
        }
        Ok(timeline)
    }
}

fn signed_optional_delta(current: Option<u32>, reference: Option<u32>) -> Option<i32> {
    match (current, reference) {
        (Some(current), Some(reference)) => Some(signed_delta(current, reference)),
        _ => None,
    }
}

fn signed_delta(current: u32, reference: u32) -> i32 {
    i64::from(current)
        .saturating_sub(i64::from(reference))
        .clamp(i64::from(i32::MIN), i64::from(i32::MAX)) as i32
}

fn dynamic_time_warping(current: &[MarkKind], reference: &[MarkKind]) -> f32 {
    if current.is_empty() || reference.is_empty() {
        return 1.0;
    }

    let width = reference.len();
    let max_length = current.len().max(reference.len());
    let band = current.len().abs_diff(reference.len()).max(max_length / 4);
    let infinity = f32::INFINITY;
    let mut previous = vec![infinity; width + 1];
    let mut row = vec![infinity; width + 1];
    previous[0] = 0.0;

    for (current_index, &current_kind) in current.iter().enumerate() {
        row.fill(infinity);
        let row_number = current_index + 1;
        let start = row_number.saturating_sub(band).max(1);
        let end = (row_number + band).min(width);

        for reference_number in start..=end {
            let substitution = kind_distance(current_kind, reference[reference_number - 1]);
            row[reference_number] = substitution
                + previous[reference_number]
                    .min(row[reference_number - 1])
                    .min(previous[reference_number - 1]);
        }
        core::mem::swap(&mut previous, &mut row);
    }

    let distance = previous[width];
    if distance.is_finite() {
        (distance / max_length as f32).clamp(0.0, 1.0)
    } else {
        1.0
    }
}

fn kind_distance(left: MarkKind, right: MarkKind) -> f32 {
    use MarkKind::{Conclusion, Evidence, Preamble, Silence};

    match (left, right) {
        (left, right) if left == right => 0.0,
        (Preamble, Evidence) | (Evidence, Preamble) => 0.45,
        (Conclusion, Evidence) | (Evidence, Conclusion) => 0.60,
        (Silence, _) | (_, Silence) => 1.0,
        _ => 0.85,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn print(marks: &[(MarkKind, u32)]) -> AnswerPrint {
        AnswerPrint::new(
            marks
                .iter()
                .map(|&(kind, duration_ms)| TimedMark { kind, duration_ms }),
        )
        .expect("valid answer print")
    }

    #[test]
    fn merges_adjacent_marks_and_finds_conclusion() {
        let answer = print(&[
            (MarkKind::Silence, 300),
            (MarkKind::Preamble, 200),
            (MarkKind::Preamble, 250),
            (MarkKind::Conclusion, 500),
            (MarkKind::Evidence, 700),
        ]);

        assert_eq!(answer.marks().len(), 4);
        assert_eq!(answer.conclusion_latency_ms(), Some(750));
        assert_eq!(answer.total_ms(), 1_950);
    }

    #[test]
    fn time_alignment_tolerates_speaking_rate_changes() {
        let baseline = print(&[
            (MarkKind::Silence, 200),
            (MarkKind::Conclusion, 400),
            (MarkKind::Evidence, 800),
        ]);
        let slower = print(&[
            (MarkKind::Silence, 300),
            (MarkKind::Conclusion, 600),
            (MarkKind::Evidence, 1_200),
        ]);

        let comparison = slower.compare(&baseline);
        assert!(comparison.structural_distance < 0.15);
        assert_eq!(comparison.conclusion_latency_delta_ms, Some(100));
    }

    #[test]
    fn detects_preamble_before_conclusion() {
        let baseline = print(&[(MarkKind::Conclusion, 400), (MarkKind::Evidence, 800)]);
        let preamble_first = print(&[
            (MarkKind::Preamble, 900),
            (MarkKind::Conclusion, 400),
            (MarkKind::Evidence, 800),
        ]);

        let comparison = preamble_first.compare(&baseline);
        assert!(comparison.structural_distance > 0.10);
        assert_eq!(comparison.pre_conclusion_delta_ms, 900);
    }

    #[test]
    fn refuses_empty_or_unbounded_profiles() {
        assert_eq!(AnswerPrint::new([]), Err(PrintError::Empty));
        assert_eq!(
            AnswerPrint::new([TimedMark {
                kind: MarkKind::Silence,
                duration_ms: MAX_ANSWER_MS + 1,
            }]),
            Err(PrintError::TooLong)
        );
    }
}
