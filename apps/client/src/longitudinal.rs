//! Explicitly opt-in, device-only evaluation ledger.
//!
//! This module intentionally does not observe normal conversations. The browser storage is not
//! opened until a person presses the long-term evaluation start button. The ledger can represent
//! only coarse, fixed-schema probe outcomes; it has no field capable of carrying audio,
//! transcripts, free text, account identifiers, exact timestamps, or latency.

use std::collections::HashSet;
use std::fmt::Write as _;

const ENVELOPE_STORAGE_KEY: &str = "kotae.evaluation.envelope.v1";
const FENCE_STORAGE_KEY: &str = "kotae.evaluation.fence.v1";
const LEGACY_CONSENT_STORAGE_KEY: &str = "kotae.evaluation.consent.v1";
const LEGACY_LEDGER_STORAGE_KEY: &str = "kotae.evaluation.ledger.v1";
const ENVELOPE_HEADER: &str = "KOTAE_EVALUATION_ENVELOPE_V1";
const FENCE_HEADER: &str = "KOTAE_EVALUATION_FENCE_V1";
const LEDGER_HEADER: &str = "KOTAE_EVALUATION_LEDGER_V1";
const LEGACY_INVALID_MARKER: &str = "invalid";
const SCHEMA_VERSION: u8 = 1;
const CURRENT_CONSENT_VERSION: u16 = 1;

/// Retains all planned observations after the final follow-up window has closed.
pub(crate) const RETENTION_DAYS: u32 = 168;
const STUDY_HORIZON_DAYS: u32 = 143;
const MAX_LEDGER_BYTES: usize = 32 * 1024;
const MAX_ENVELOPE_BYTES: usize = MAX_LEDGER_BYTES + 256;
const MAX_EVENTS: usize = 256;
const MAX_LOGICAL_DAY: u32 = 100_000;
const PROBE_BANK_VERSION: &str = "a-first-v1";
const POLICY_VERSION: &str = "within-person-v1";
const SCORER_VERSION: &str = "self-report-v1";
const MODEL_VERSION: &str = "no-model-v1";

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum EvaluationState {
    /// The privacy-preserving initial state. Constructing it does not access browser storage.
    Dormant,
    Active(EvaluationLedgerV1),
    Withdrawn,
    Deleted,
    Invalid,
    StorageUnavailable,
}

impl Default for EvaluationState {
    fn default() -> Self {
        Self::Dormant
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum EvaluationView {
    Dormant,
    Active { event_count: usize },
    Withdrawn,
    Deleted,
    Invalid,
    StorageUnavailable,
}

/// A display-only projection of one fixed-schema observation.
///
/// It deliberately excludes participant metadata, storage identifiers, exact dates, question
/// text, and every field that could carry audio or free text.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct EvaluationObservationView {
    pub(crate) timepoint: &'static str,
    pub(crate) outcome: &'static str,
    pub(crate) enjoyment: &'static str,
    pub(crate) agency: &'static str,
    pub(crate) burden: &'static str,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ScheduleView {
    NotActive,
    Due {
        timepoint: &'static str,
        question: &'static str,
        days_remaining: u32,
        completed: usize,
    },
    Waiting {
        next_timepoint: &'static str,
        days_until: u32,
        completed: usize,
        missed: usize,
    },
    Complete {
        completed: usize,
        missed: usize,
    },
    ClockUnavailable,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RecordFailure {
    NotActive,
    NotDue,
    Duplicate,
    OutsideDeadline,
    StorageUnavailable,
    InvalidLedger,
}

impl RecordFailure {
    pub(crate) const fn message(self) -> &'static str {
        match self {
            Self::NotActive => "測定を開始または再開してから記録してください。",
            Self::NotDue => "いま回答できる固定質問はありません。期限外の記録は保存しません。",
            Self::Duplicate => "この回はすでに記録済みです。重複記録は保存しません。",
            Self::OutsideDeadline => "回答期限外のため保存しませんでした。",
            Self::StorageUnavailable => "この端末の保存領域を安全に確認できませんでした。",
            Self::InvalidLedger => "端末内の測定記録を検証できません。全削除してやり直せます。",
        }
    }
}

impl EvaluationState {
    pub(crate) fn view(&self) -> EvaluationView {
        match self {
            Self::Dormant => EvaluationView::Dormant,
            Self::Active(ledger) => EvaluationView::Active {
                event_count: ledger.events.len(),
            },
            Self::Withdrawn => EvaluationView::Withdrawn,
            Self::Deleted => EvaluationView::Deleted,
            Self::Invalid => EvaluationView::Invalid,
            Self::StorageUnavailable => EvaluationView::StorageUnavailable,
        }
    }

    /// Returns the raw finite-category observations in the planned timepoint order.
    ///
    /// No composite score, change score, improvement classification, or causal interpretation is
    /// calculated here.
    pub(crate) fn observations(&self) -> Vec<EvaluationObservationView> {
        let Self::Active(ledger) = self else {
            return Vec::new();
        };

        EvaluationTimepoint::ALL
            .into_iter()
            .filter_map(|timepoint| {
                ledger
                    .events
                    .iter()
                    .find(|event| event.timepoint == timepoint)
                    .map(|event| EvaluationObservationView {
                        timepoint: event.timepoint.label(),
                        outcome: event.outcome.label(),
                        enjoyment: event.enjoyment.label(),
                        agency: event.agency.label(),
                        burden: event.burden.label(),
                    })
            })
            .collect()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
enum EvaluationTimepoint {
    Baseline,
    Wk4,
    Wk8,
    Followup4,
    Followup12,
}

impl EvaluationTimepoint {
    const ALL: [Self; 5] = [
        Self::Baseline,
        Self::Wk4,
        Self::Wk8,
        Self::Followup4,
        Self::Followup12,
    ];

    const fn as_wire(self) -> &'static str {
        match self {
            Self::Baseline => "baseline",
            Self::Wk4 => "wk4",
            Self::Wk8 => "wk8",
            Self::Followup4 => "followup4",
            Self::Followup12 => "followup12",
        }
    }

    fn parse(value: &str) -> Result<Self, DecodeError> {
        match value {
            "baseline" => Ok(Self::Baseline),
            "wk4" => Ok(Self::Wk4),
            "wk8" => Ok(Self::Wk8),
            "followup4" => Ok(Self::Followup4),
            "followup12" => Ok(Self::Followup12),
            _ => Err(DecodeError::UnknownEnum),
        }
    }

    const fn label(self) -> &'static str {
        match self {
            Self::Baseline => "開始時",
            Self::Wk4 => "4週目",
            Self::Wk8 => "8週目",
            Self::Followup4 => "終了4週後",
            Self::Followup12 => "終了12週後",
        }
    }

    const fn window(self) -> (u32, u32) {
        match self {
            Self::Baseline => (0, 3),
            Self::Wk4 => (25, 35),
            Self::Wk8 => (53, 63),
            Self::Followup4 => (81, 91),
            Self::Followup12 => (133, 143),
        }
    }

    const fn probe_id(self) -> &'static str {
        match self {
            Self::Baseline => "direct-b01",
            Self::Wk4 => "direct-w04",
            Self::Wk8 => "direct-w08",
            Self::Followup4 => "direct-f04",
            Self::Followup12 => "direct-f12",
        }
    }

    const fn question(self) -> &'static str {
        match self {
            Self::Baseline => "朝に飲むものを一つ挙げ、その理由を一つ話してください。",
            Self::Wk4 => "雨の日に持っていく物を一つ挙げ、その理由を一つ話してください。",
            Self::Wk8 => "短い休憩にしたいことを一つ挙げ、その理由を一つ話してください。",
            Self::Followup4 => "人に勧めたい身近な場所を一つ挙げ、その理由を一つ話してください。",
            Self::Followup12 => {
                "今週先に終わらせたいことを一つ挙げ、その理由を一つ話してください。"
            }
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum EvaluationOutcome {
    AFirst,
    ValidAbstention,
    NotAFirst,
    Unscorable,
}

impl EvaluationOutcome {
    pub(crate) const ALL: [Self; 4] = [
        Self::AFirst,
        Self::ValidAbstention,
        Self::NotAFirst,
        Self::Unscorable,
    ];

    pub(crate) const fn label(self) -> &'static str {
        match self {
            Self::AFirst => "結論を先に言えた",
            Self::ValidAbstention => "分からないと先に言えた",
            Self::NotAFirst => "理由や前置きが先だった",
            Self::Unscorable => "分類できない／聞き取り失敗",
        }
    }

    const fn as_wire(self) -> &'static str {
        match self {
            Self::AFirst => "a_first",
            Self::ValidAbstention => "valid_abstention",
            Self::NotAFirst => "not_a_first",
            Self::Unscorable => "unscorable",
        }
    }

    fn parse(value: &str) -> Result<Self, DecodeError> {
        match value {
            "a_first" => Ok(Self::AFirst),
            "valid_abstention" => Ok(Self::ValidAbstention),
            "not_a_first" => Ok(Self::NotAFirst),
            "unscorable" => Ok(Self::Unscorable),
            _ => Err(DecodeError::UnknownEnum),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum AsrUsability {
    Usable,
    Failed,
}

impl AsrUsability {
    const fn as_wire(self) -> &'static str {
        match self {
            Self::Usable => "usable",
            Self::Failed => "failed",
        }
    }

    fn parse(value: &str) -> Result<Self, DecodeError> {
        match value {
            "usable" => Ok(Self::Usable),
            "failed" => Ok(Self::Failed),
            _ => Err(DecodeError::UnknownEnum),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum OptionalRating {
    Missing,
    One,
    Two,
    Three,
    Four,
    Five,
}

impl OptionalRating {
    pub(crate) const ALL: [Self; 6] = [
        Self::Missing,
        Self::One,
        Self::Two,
        Self::Three,
        Self::Four,
        Self::Five,
    ];

    pub(crate) const fn label(self) -> &'static str {
        match self {
            Self::Missing => "回答しない",
            Self::One => "1",
            Self::Two => "2",
            Self::Three => "3",
            Self::Four => "4",
            Self::Five => "5",
        }
    }

    const fn as_wire(self) -> &'static str {
        match self {
            Self::Missing => "missing",
            Self::One => "1",
            Self::Two => "2",
            Self::Three => "3",
            Self::Four => "4",
            Self::Five => "5",
        }
    }

    fn parse(value: &str) -> Result<Self, DecodeError> {
        match value {
            "missing" => Ok(Self::Missing),
            "1" => Ok(Self::One),
            "2" => Ok(Self::Two),
            "3" => Ok(Self::Three),
            "4" => Ok(Self::Four),
            "5" => Ok(Self::Five),
            _ => Err(DecodeError::UnknownEnum),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
struct LogicalDay(u32);

impl LogicalDay {
    fn parse(value: &str) -> Result<Self, DecodeError> {
        if value.is_empty() || (value.len() > 1 && value.starts_with('0')) {
            return Err(DecodeError::InvalidField);
        }
        let day = value
            .parse::<u32>()
            .map_err(|_| DecodeError::InvalidField)?;
        if day > MAX_LOGICAL_DAY || day.to_string() != value {
            return Err(DecodeError::InvalidField);
        }
        Ok(Self(day))
    }

    fn days_since(self, earlier: Self) -> Option<u32> {
        self.0.checked_sub(earlier.0)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
struct ParticipantId(String);

#[derive(Clone, Debug, PartialEq, Eq)]
struct ConsentEpoch(String);

#[derive(Clone, Debug, PartialEq, Eq)]
struct StorageGeneration(String);

impl ParticipantId {
    fn parse(value: &str) -> Result<Self, DecodeError> {
        parse_hex_identifier(value).map(Self)
    }

    fn as_str(&self) -> &str {
        &self.0
    }
}

impl ConsentEpoch {
    fn parse(value: &str) -> Result<Self, DecodeError> {
        parse_hex_identifier(value).map(Self)
    }

    fn as_str(&self) -> &str {
        &self.0
    }
}

impl StorageGeneration {
    fn parse(value: &str) -> Result<Self, DecodeError> {
        parse_hex_identifier(value).map(Self)
    }

    fn as_str(&self) -> &str {
        &self.0
    }
}

fn parse_hex_identifier(value: &str) -> Result<String, DecodeError> {
    if value.len() == 32
        && value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        Ok(value.to_owned())
    } else {
        Err(DecodeError::InvalidField)
    }
}

macro_rules! bounded_token {
    ($name:ident, $max:expr) => {
        #[derive(Clone, Debug, PartialEq, Eq, Hash)]
        struct $name(String);

        impl $name {
            fn parse(value: &str) -> Result<Self, DecodeError> {
                if valid_bounded_token(value, $max) {
                    Ok(Self(value.to_owned()))
                } else {
                    Err(DecodeError::InvalidField)
                }
            }

            fn as_str(&self) -> &str {
                &self.0
            }
        }
    };
}

bounded_token!(ProbeId, 32);
bounded_token!(ProbeBankVersion, 24);
bounded_token!(PolicyVersion, 24);
bounded_token!(ScorerVersion, 24);
bounded_token!(ModelVersion, 32);

fn valid_bounded_token(value: &str, maximum: usize) -> bool {
    let bytes = value.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= maximum
        && bytes[0].is_ascii_alphanumeric()
        && bytes.iter().all(|byte| {
            byte.is_ascii_lowercase() || byte.is_ascii_digit() || matches!(byte, b'.' | b'_' | b'-')
        })
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct EvaluationEvent {
    day: LogicalDay,
    timepoint: EvaluationTimepoint,
    probe_bank_version: ProbeBankVersion,
    probe_id: ProbeId,
    outcome: EvaluationOutcome,
    asr: AsrUsability,
    enjoyment: OptionalRating,
    agency: OptionalRating,
    burden: OptionalRating,
    policy_version: PolicyVersion,
    scorer_version: ScorerVersion,
    model_version: ModelVersion,
}

impl EvaluationEvent {
    #[allow(clippy::too_many_arguments)]
    fn new(
        day: LogicalDay,
        timepoint: EvaluationTimepoint,
        probe_bank_version: ProbeBankVersion,
        probe_id: ProbeId,
        outcome: EvaluationOutcome,
        asr: AsrUsability,
        enjoyment: OptionalRating,
        agency: OptionalRating,
        burden: OptionalRating,
        policy_version: PolicyVersion,
        scorer_version: ScorerVersion,
        model_version: ModelVersion,
    ) -> Result<Self, RecordError> {
        if asr == AsrUsability::Failed && outcome != EvaluationOutcome::Unscorable {
            return Err(RecordError::InvalidCombination);
        }
        Ok(Self {
            day,
            timepoint,
            probe_bank_version,
            probe_id,
            outcome,
            asr,
            enjoyment,
            agency,
            burden,
            policy_version,
            scorer_version,
            model_version,
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct EvaluationLedgerV1 {
    participant_id: ParticipantId,
    consent_version: u16,
    consent_epoch: ConsentEpoch,
    enrolled_day: LogicalDay,
    events: Vec<EvaluationEvent>,
}

impl EvaluationLedgerV1 {
    fn new(
        participant_id: ParticipantId,
        consent_epoch: ConsentEpoch,
        enrolled_day: LogicalDay,
    ) -> Self {
        Self {
            participant_id,
            consent_version: CURRENT_CONSENT_VERSION,
            consent_epoch,
            enrolled_day,
            events: Vec::new(),
        }
    }

    fn record(
        &mut self,
        event: EvaluationEvent,
        access_day: LogicalDay,
    ) -> Result<(), RecordError> {
        self.prune(access_day);

        if event.day > access_day || event.day < self.enrolled_day {
            return Err(RecordError::OutsideStudyWindow);
        }
        let Some(study_day) = event.day.days_since(self.enrolled_day) else {
            return Err(RecordError::OutsideStudyWindow);
        };
        let (window_start, window_end) = event.timepoint.window();
        if study_day > STUDY_HORIZON_DAYS || !(window_start..=window_end).contains(&study_day) {
            return Err(RecordError::OutsideStudyWindow);
        }
        if event.probe_bank_version.as_str() != PROBE_BANK_VERSION
            || event.probe_id.as_str() != event.timepoint.probe_id()
            || event.policy_version.as_str() != POLICY_VERSION
            || event.scorer_version.as_str() != SCORER_VERSION
            || event.model_version.as_str() != MODEL_VERSION
        {
            return Err(RecordError::InvalidDefinition);
        }
        if self
            .events
            .iter()
            .any(|stored| stored.timepoint == event.timepoint)
        {
            return Err(RecordError::DuplicateProbeAtTimepoint);
        }
        if self.events.len() >= MAX_EVENTS {
            return Err(RecordError::EventLimit);
        }
        self.events.push(event);
        Ok(())
    }

    /// Access-time pruning keeps 168 logical days (age 0 through 167) and drops future entries.
    fn prune(&mut self, access_day: LogicalDay) -> bool {
        let before = self.events.len();
        self.events.retain(|event| {
            access_day
                .days_since(event.day)
                .is_some_and(|age| age < RETENTION_DAYS)
        });
        before != self.events.len()
    }

    fn encode(&self) -> Result<String, EncodeError> {
        let mut encoded = String::with_capacity(256 + self.events.len() * 128);
        writeln!(&mut encoded, "{LEDGER_HEADER}").map_err(|_| EncodeError::Formatting)?;
        writeln!(
            &mut encoded,
            "M\t{}\t{}\t{}\t{}\t{}",
            SCHEMA_VERSION,
            self.consent_version,
            self.participant_id.as_str(),
            self.consent_epoch.as_str(),
            self.enrolled_day.0,
        )
        .map_err(|_| EncodeError::Formatting)?;

        for event in &self.events {
            writeln!(
                &mut encoded,
                "E\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}",
                event.day.0,
                event.timepoint.as_wire(),
                event.probe_bank_version.as_str(),
                event.probe_id.as_str(),
                event.outcome.as_wire(),
                event.asr.as_wire(),
                event.enjoyment.as_wire(),
                event.agency.as_wire(),
                event.burden.as_wire(),
                event.policy_version.as_str(),
                event.scorer_version.as_str(),
                event.model_version.as_str(),
            )
            .map_err(|_| EncodeError::Formatting)?;
        }
        if encoded.len() > MAX_LEDGER_BYTES {
            return Err(EncodeError::TooLarge);
        }
        Ok(encoded)
    }

    fn decode(encoded: &str) -> Result<Self, DecodeError> {
        if encoded.len() > MAX_LEDGER_BYTES {
            return Err(DecodeError::TooLarge);
        }
        if encoded.contains('\r') || !encoded.ends_with('\n') {
            return Err(DecodeError::InvalidShape);
        }
        let mut lines = encoded.lines();
        if lines.next() != Some(LEDGER_HEADER) {
            return Err(DecodeError::InvalidShape);
        }

        let metadata = lines.next().ok_or(DecodeError::InvalidShape)?;
        let metadata = metadata.split('\t').collect::<Vec<_>>();
        if metadata.len() != 6 || metadata[0] != "M" {
            return Err(DecodeError::InvalidShape);
        }
        if metadata[1] != SCHEMA_VERSION.to_string()
            || metadata[2] != CURRENT_CONSENT_VERSION.to_string()
        {
            return Err(DecodeError::UnsupportedVersion);
        }

        let participant_id = ParticipantId::parse(metadata[3])?;
        let consent_epoch = ConsentEpoch::parse(metadata[4])?;
        let enrolled_day = LogicalDay::parse(metadata[5])?;
        let mut events = Vec::new();
        let mut seen = HashSet::new();

        for line in lines {
            if events.len() >= MAX_EVENTS {
                return Err(DecodeError::TooManyEvents);
            }
            let fields = line.split('\t').collect::<Vec<_>>();
            if fields.len() != 13 || fields[0] != "E" {
                return Err(DecodeError::InvalidShape);
            }
            let day = LogicalDay::parse(fields[1])?;
            let timepoint = EvaluationTimepoint::parse(fields[2])?;
            let probe_bank_version = ProbeBankVersion::parse(fields[3])?;
            let probe_id = ProbeId::parse(fields[4])?;
            let outcome = EvaluationOutcome::parse(fields[5])?;
            let asr = AsrUsability::parse(fields[6])?;
            let enjoyment = OptionalRating::parse(fields[7])?;
            let agency = OptionalRating::parse(fields[8])?;
            let burden = OptionalRating::parse(fields[9])?;
            let policy_version = PolicyVersion::parse(fields[10])?;
            let scorer_version = ScorerVersion::parse(fields[11])?;
            let model_version = ModelVersion::parse(fields[12])?;
            let event = EvaluationEvent::new(
                day,
                timepoint,
                probe_bank_version,
                probe_id.clone(),
                outcome,
                asr,
                enjoyment,
                agency,
                burden,
                policy_version,
                scorer_version,
                model_version,
            )
            .map_err(|_| DecodeError::InvalidField)?;

            let Some(study_day) = event.day.days_since(enrolled_day) else {
                return Err(DecodeError::InvalidField);
            };
            let (window_start, window_end) = timepoint.window();
            if study_day > STUDY_HORIZON_DAYS
                || !(window_start..=window_end).contains(&study_day)
                || event.probe_bank_version.as_str() != PROBE_BANK_VERSION
                || event.probe_id.as_str() != timepoint.probe_id()
                || event.policy_version.as_str() != POLICY_VERSION
                || event.scorer_version.as_str() != SCORER_VERSION
                || event.model_version.as_str() != MODEL_VERSION
                || !seen.insert(timepoint)
            {
                return Err(DecodeError::InvalidField);
            }
            events.push(event);
        }

        Ok(Self {
            participant_id,
            consent_version: CURRENT_CONSENT_VERSION,
            consent_epoch,
            enrolled_day,
            events,
        })
    }
}

fn due_timepoint(ledger: &EvaluationLedgerV1, today: LogicalDay) -> Option<EvaluationTimepoint> {
    let study_day = today.days_since(ledger.enrolled_day)?;
    EvaluationTimepoint::ALL.into_iter().find(|timepoint| {
        let (start, end) = timepoint.window();
        (start..=end).contains(&study_day)
            && !ledger
                .events
                .iter()
                .any(|event| event.timepoint == *timepoint)
    })
}

fn schedule_for_day(ledger: &EvaluationLedgerV1, today: LogicalDay) -> ScheduleView {
    let Some(study_day) = today.days_since(ledger.enrolled_day) else {
        return ScheduleView::ClockUnavailable;
    };
    let completed = ledger.events.len();
    let mut missed = 0;

    for timepoint in EvaluationTimepoint::ALL {
        if ledger
            .events
            .iter()
            .any(|event| event.timepoint == timepoint)
        {
            continue;
        }
        let (start, end) = timepoint.window();
        if study_day < start {
            return ScheduleView::Waiting {
                next_timepoint: timepoint.label(),
                days_until: start - study_day,
                completed,
                missed,
            };
        }
        if study_day <= end {
            return ScheduleView::Due {
                timepoint: timepoint.label(),
                question: timepoint.question(),
                days_remaining: end - study_day,
                completed,
            };
        }
        missed += 1;
    }

    ScheduleView::Complete { completed, missed }
}

pub(crate) fn schedule(state: &EvaluationState) -> ScheduleView {
    let EvaluationState::Active(ledger) = state else {
        return ScheduleView::NotActive;
    };

    #[cfg(target_arch = "wasm32")]
    {
        let Ok(today) = browser_logical_day() else {
            return ScheduleView::ClockUnavailable;
        };
        schedule_for_day(ledger, today)
    }

    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = ledger;
        ScheduleView::ClockUnavailable
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum EnvelopeState {
    Active(EvaluationLedgerV1),
    Withdrawn(EvaluationLedgerV1),
    Deleted,
    Invalid,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct EvaluationEnvelopeV1 {
    generation: StorageGeneration,
    state: EnvelopeState,
}

impl EvaluationEnvelopeV1 {
    fn active(generation: StorageGeneration, ledger: EvaluationLedgerV1) -> Self {
        Self {
            generation,
            state: EnvelopeState::Active(ledger),
        }
    }

    fn encode(&self) -> Result<String, EncodeError> {
        let state = match self.state {
            EnvelopeState::Active(_) => "active",
            EnvelopeState::Withdrawn(_) => "withdrawn",
            EnvelopeState::Deleted => "deleted",
            EnvelopeState::Invalid => "invalid",
        };
        let mut encoded = format!(
            "{ENVELOPE_HEADER}\nS\t{state}\t{}\n",
            self.generation.as_str()
        );
        match &self.state {
            EnvelopeState::Active(ledger) | EnvelopeState::Withdrawn(ledger) => {
                encoded.push_str(&ledger.encode()?);
            }
            EnvelopeState::Deleted | EnvelopeState::Invalid => {}
        }
        if encoded.len() > MAX_ENVELOPE_BYTES {
            return Err(EncodeError::TooLarge);
        }
        Ok(encoded)
    }

    fn decode(encoded: &str) -> Result<Self, DecodeError> {
        if encoded.len() > MAX_ENVELOPE_BYTES || encoded.contains('\r') || !encoded.ends_with('\n')
        {
            return Err(DecodeError::InvalidShape);
        }
        let mut sections = encoded.splitn(3, '\n');
        if sections.next() != Some(ENVELOPE_HEADER) {
            return Err(DecodeError::InvalidShape);
        }
        let status = sections.next().ok_or(DecodeError::InvalidShape)?;
        let payload = sections.next().ok_or(DecodeError::InvalidShape)?;
        let fields = status.split('\t').collect::<Vec<_>>();
        if fields.len() != 3 || fields[0] != "S" {
            return Err(DecodeError::InvalidShape);
        }
        let generation = StorageGeneration::parse(fields[2])?;
        let state = match fields[1] {
            "active" if !payload.is_empty() => {
                EnvelopeState::Active(EvaluationLedgerV1::decode(payload)?)
            }
            "withdrawn" if !payload.is_empty() => {
                EnvelopeState::Withdrawn(EvaluationLedgerV1::decode(payload)?)
            }
            "deleted" if payload.is_empty() => EnvelopeState::Deleted,
            "invalid" if payload.is_empty() => EnvelopeState::Invalid,
            "active" | "withdrawn" | "deleted" | "invalid" => {
                return Err(DecodeError::InvalidShape);
            }
            _ => return Err(DecodeError::UnknownEnum),
        };
        Ok(Self { generation, state })
    }

    fn into_evaluation_state(self) -> EvaluationState {
        match self.state {
            EnvelopeState::Active(ledger) => EvaluationState::Active(ledger),
            EnvelopeState::Withdrawn(_) => EvaluationState::Withdrawn,
            EnvelopeState::Deleted => EvaluationState::Deleted,
            EnvelopeState::Invalid => EvaluationState::Invalid,
        }
    }
}

fn encode_fence(generation: &StorageGeneration) -> String {
    format!("{FENCE_HEADER}:{}", generation.as_str())
}

fn decode_fence(value: &str) -> Result<StorageGeneration, DecodeError> {
    let prefix = format!("{FENCE_HEADER}:");
    let generation = value
        .strip_prefix(&prefix)
        .ok_or(DecodeError::InvalidShape)?;
    StorageGeneration::parse(generation)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RecordError {
    DuplicateProbeAtTimepoint,
    OutsideStudyWindow,
    EventLimit,
    InvalidCombination,
    InvalidDefinition,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum DecodeError {
    TooLarge,
    TooManyEvents,
    InvalidShape,
    InvalidField,
    UnknownEnum,
    UnsupportedVersion,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum EncodeError {
    Formatting,
    TooLarge,
}

trait EvaluationStorage {
    fn get(&self, key: &str) -> Result<Option<String>, StorageError>;
    fn set(&self, key: &str, value: &str) -> Result<(), StorageError>;
    fn remove(&self, key: &str) -> Result<(), StorageError>;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct StorageError;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum EnvelopeLoadError {
    Storage,
    Invalid,
}

fn read_envelope<S: EvaluationStorage>(
    store: &S,
) -> Result<Option<(String, EvaluationEnvelopeV1)>, EnvelopeLoadError> {
    let raw = store
        .get(ENVELOPE_STORAGE_KEY)
        .map_err(|_| EnvelopeLoadError::Storage)?;
    let fence = store
        .get(FENCE_STORAGE_KEY)
        .map_err(|_| EnvelopeLoadError::Storage)?;
    match (raw, fence) {
        (None, None) => Ok(None),
        (Some(raw), Some(fence)) => {
            let envelope =
                EvaluationEnvelopeV1::decode(&raw).map_err(|_| EnvelopeLoadError::Invalid)?;
            let fence = decode_fence(&fence).map_err(|_| EnvelopeLoadError::Invalid)?;
            if fence != envelope.generation {
                return Err(EnvelopeLoadError::Invalid);
            }
            Ok(Some((raw, envelope)))
        }
        _ => Err(EnvelopeLoadError::Invalid),
    }
}

fn owns_fence<S: EvaluationStorage>(store: &S, generation: &StorageGeneration) -> bool {
    store
        .get(FENCE_STORAGE_KEY)
        .ok()
        .flatten()
        .is_some_and(|value| value == encode_fence(generation))
}

fn seal_invalid_if_owner<S: EvaluationStorage>(store: &S, generation: &StorageGeneration) {
    if !owns_fence(store, generation) {
        return;
    }
    let invalid = EvaluationEnvelopeV1 {
        generation: generation.clone(),
        state: EnvelopeState::Invalid,
    };
    if let Ok(encoded) = invalid.encode() {
        let _ = store.set(ENVELOPE_STORAGE_KEY, &encoded);
    }
}

/// Claims a fresh opaque generation before replacing the single consent+ledger envelope.
/// If another tab changes either value during the operation, the owner seals the envelope
/// invalid instead of letting a stale active ledger become authoritative.
fn install_envelope<S: EvaluationStorage>(
    store: &S,
    expected_raw: Option<&str>,
    replacement: &EvaluationEnvelopeV1,
) -> Result<(), StorageError> {
    let replacement_raw = replacement.encode().map_err(|_| StorageError)?;
    if store.get(ENVELOPE_STORAGE_KEY)?.as_deref() != expected_raw {
        return Err(StorageError);
    }
    if let Some(raw) = expected_raw {
        let expected = EvaluationEnvelopeV1::decode(raw).map_err(|_| StorageError)?;
        let expected_fence = encode_fence(&expected.generation);
        if store.get(FENCE_STORAGE_KEY)?.as_deref() != Some(expected_fence.as_str()) {
            return Err(StorageError);
        }
    } else if store.get(FENCE_STORAGE_KEY)?.is_some() {
        return Err(StorageError);
    }

    store.set(FENCE_STORAGE_KEY, &encode_fence(&replacement.generation))?;
    if !owns_fence(store, &replacement.generation)
        || store.get(ENVELOPE_STORAGE_KEY)?.as_deref() != expected_raw
    {
        seal_invalid_if_owner(store, &replacement.generation);
        return Err(StorageError);
    }
    store.set(ENVELOPE_STORAGE_KEY, &replacement_raw)?;
    if !owns_fence(store, &replacement.generation)
        || store.get(ENVELOPE_STORAGE_KEY)?.as_deref() != Some(replacement_raw.as_str())
    {
        seal_invalid_if_owner(store, &replacement.generation);
        return Err(StorageError);
    }
    Ok(())
}

fn force_tombstone<S: EvaluationStorage>(
    store: &S,
    generation: StorageGeneration,
    state: EnvelopeState,
) -> EvaluationState {
    if store
        .set(FENCE_STORAGE_KEY, &encode_fence(&generation))
        .is_err()
    {
        return EvaluationState::StorageUnavailable;
    }
    let envelope = EvaluationEnvelopeV1 { generation, state };
    let Ok(encoded) = envelope.encode() else {
        return EvaluationState::StorageUnavailable;
    };
    if store.set(ENVELOPE_STORAGE_KEY, &encoded).is_err()
        || !owns_fence(store, &envelope.generation)
        || store.get(ENVELOPE_STORAGE_KEY).ok().flatten().as_deref() != Some(encoded.as_str())
    {
        seal_invalid_if_owner(store, &envelope.generation);
        EvaluationState::StorageUnavailable
    } else {
        envelope.into_evaluation_state()
    }
}

fn record_due_with_store<S: EvaluationStorage>(
    store: &S,
    expected: &EvaluationLedgerV1,
    today: LogicalDay,
    new_generation: StorageGeneration,
    outcome: EvaluationOutcome,
    enjoyment: OptionalRating,
    agency: OptionalRating,
    burden: OptionalRating,
) -> Result<EvaluationState, RecordFailure> {
    let (expected_raw, envelope) = match read_envelope(store) {
        Ok(Some(value)) => value,
        Ok(None) | Err(EnvelopeLoadError::Invalid) => {
            return Err(RecordFailure::InvalidLedger);
        }
        Err(EnvelopeLoadError::Storage) => return Err(RecordFailure::StorageUnavailable),
    };
    let EnvelopeState::Active(mut ledger) = envelope.state else {
        return Err(RecordFailure::NotActive);
    };
    if ledger != *expected {
        return Err(RecordFailure::InvalidLedger);
    }

    let Some(timepoint) = due_timepoint(&ledger, today) else {
        if ledger.events.iter().any(|event| {
            let Some(study_day) = today.days_since(ledger.enrolled_day) else {
                return false;
            };
            let (start, end) = event.timepoint.window();
            (start..=end).contains(&study_day)
                && event.timepoint.probe_id() == event.probe_id.as_str()
        }) {
            return Err(RecordFailure::Duplicate);
        }
        return Err(RecordFailure::NotDue);
    };

    let asr = if outcome == EvaluationOutcome::Unscorable {
        AsrUsability::Failed
    } else {
        AsrUsability::Usable
    };
    let event = EvaluationEvent::new(
        today,
        timepoint,
        ProbeBankVersion::parse(PROBE_BANK_VERSION).map_err(|_| RecordFailure::InvalidLedger)?,
        ProbeId::parse(timepoint.probe_id()).map_err(|_| RecordFailure::InvalidLedger)?,
        outcome,
        asr,
        enjoyment,
        agency,
        burden,
        PolicyVersion::parse(POLICY_VERSION).map_err(|_| RecordFailure::InvalidLedger)?,
        ScorerVersion::parse(SCORER_VERSION).map_err(|_| RecordFailure::InvalidLedger)?,
        ModelVersion::parse(MODEL_VERSION).map_err(|_| RecordFailure::InvalidLedger)?,
    )
    .map_err(|_| RecordFailure::InvalidLedger)?;

    ledger.record(event, today).map_err(|error| match error {
        RecordError::DuplicateProbeAtTimepoint => RecordFailure::Duplicate,
        RecordError::OutsideStudyWindow => RecordFailure::OutsideDeadline,
        RecordError::EventLimit
        | RecordError::InvalidCombination
        | RecordError::InvalidDefinition => RecordFailure::InvalidLedger,
    })?;
    let replacement = EvaluationEnvelopeV1::active(new_generation, ledger.clone());
    install_envelope(store, Some(&expected_raw), &replacement)
        .map_err(|_| RecordFailure::StorageUnavailable)?;
    Ok(EvaluationState::Active(ledger))
}

fn start_with_store<S: EvaluationStorage>(
    store: &S,
    today: LogicalDay,
    new_participant_id: ParticipantId,
    new_consent_epoch: ConsentEpoch,
    new_generation: StorageGeneration,
) -> EvaluationState {
    let loaded = match read_envelope(store) {
        Ok(value) => value,
        Err(EnvelopeLoadError::Storage) => return EvaluationState::StorageUnavailable,
        Err(EnvelopeLoadError::Invalid) => return EvaluationState::Invalid,
    };
    match loaded {
        None => {
            let legacy_consent = match store.get(LEGACY_CONSENT_STORAGE_KEY) {
                Ok(value) => value,
                Err(_) => return EvaluationState::StorageUnavailable,
            };
            let legacy_ledger = match store.get(LEGACY_LEDGER_STORAGE_KEY) {
                Ok(value) => value,
                Err(_) => return EvaluationState::StorageUnavailable,
            };
            // Legacy two-key state cannot be migrated atomically while an old tab may
            // still write it. Quarantine it without reading its ledger as current data;
            // explicit full deletion is required before a new study can start.
            if legacy_consent.is_some() || legacy_ledger.is_some() {
                return EvaluationState::Invalid;
            }
            let ledger = EvaluationLedgerV1::new(new_participant_id, new_consent_epoch, today);
            let replacement = EvaluationEnvelopeV1::active(new_generation, ledger.clone());
            if install_envelope(store, None, &replacement).is_err() {
                EvaluationState::StorageUnavailable
            } else {
                EvaluationState::Active(ledger)
            }
        }
        Some((raw, envelope)) => match envelope.state {
            EnvelopeState::Active(mut ledger) => {
                if ledger.prune(today) {
                    let replacement = EvaluationEnvelopeV1::active(new_generation, ledger.clone());
                    if install_envelope(store, Some(&raw), &replacement).is_err() {
                        return EvaluationState::StorageUnavailable;
                    }
                }
                EvaluationState::Active(ledger)
            }
            EnvelopeState::Withdrawn(mut ledger) => {
                ledger.prune(today);
                ledger.consent_epoch = new_consent_epoch;
                ledger.consent_version = CURRENT_CONSENT_VERSION;
                let replacement = EvaluationEnvelopeV1::active(new_generation, ledger.clone());
                if install_envelope(store, Some(&raw), &replacement).is_err() {
                    EvaluationState::StorageUnavailable
                } else {
                    EvaluationState::Active(ledger)
                }
            }
            EnvelopeState::Deleted => {
                let ledger = EvaluationLedgerV1::new(new_participant_id, new_consent_epoch, today);
                let replacement = EvaluationEnvelopeV1::active(new_generation, ledger.clone());
                if install_envelope(store, Some(&raw), &replacement).is_err() {
                    EvaluationState::StorageUnavailable
                } else {
                    EvaluationState::Active(ledger)
                }
            }
            EnvelopeState::Invalid => EvaluationState::Invalid,
        },
    }
}

fn withdraw_with_store<S: EvaluationStorage>(
    store: &S,
    new_generation: StorageGeneration,
) -> EvaluationState {
    let loaded = match read_envelope(store) {
        Ok(value) => value,
        Err(EnvelopeLoadError::Storage) => return EvaluationState::StorageUnavailable,
        Err(EnvelopeLoadError::Invalid) => return EvaluationState::Invalid,
    };
    let Some((raw, envelope)) = loaded else {
        return EvaluationState::Dormant;
    };
    match envelope.state {
        EnvelopeState::Active(ledger) => {
            let replacement = EvaluationEnvelopeV1 {
                generation: new_generation,
                state: EnvelopeState::Withdrawn(ledger),
            };
            if install_envelope(store, Some(&raw), &replacement).is_err() {
                EvaluationState::StorageUnavailable
            } else {
                EvaluationState::Withdrawn
            }
        }
        EnvelopeState::Withdrawn(_) => EvaluationState::Withdrawn,
        EnvelopeState::Deleted => EvaluationState::Deleted,
        EnvelopeState::Invalid => EvaluationState::Invalid,
    }
}

fn delete_with_store<S: EvaluationStorage>(
    store: &S,
    new_generation: StorageGeneration,
) -> EvaluationState {
    // Keep only fixed tombstones. No participant ID, consent epoch, answer, or
    // question survives a successful full delete.
    if store.remove(LEGACY_LEDGER_STORAGE_KEY).is_err()
        || store
            .set(LEGACY_CONSENT_STORAGE_KEY, LEGACY_INVALID_MARKER)
            .is_err()
    {
        return EvaluationState::StorageUnavailable;
    }
    force_tombstone(store, new_generation, EnvelopeState::Deleted)
}

#[cfg(target_arch = "wasm32")]
struct BrowserStorage {
    storage: wasm_bindgen::JsValue,
}

#[cfg(target_arch = "wasm32")]
impl BrowserStorage {
    fn open() -> Result<Self, StorageError> {
        use wasm_bindgen::JsValue;

        let window = web_sys::window().ok_or(StorageError)?;
        let storage = js_sys::Reflect::get(window.as_ref(), &JsValue::from_str("localStorage"))
            .map_err(|_| StorageError)?;
        if storage.is_null() || storage.is_undefined() {
            Err(StorageError)
        } else {
            Ok(Self { storage })
        }
    }

    fn method(&self, name: &str) -> Result<js_sys::Function, StorageError> {
        use wasm_bindgen::{JsCast, JsValue};

        js_sys::Reflect::get(&self.storage, &JsValue::from_str(name))
            .map_err(|_| StorageError)?
            .dyn_into::<js_sys::Function>()
            .map_err(|_| StorageError)
    }
}

#[cfg(target_arch = "wasm32")]
impl EvaluationStorage for BrowserStorage {
    fn get(&self, key: &str) -> Result<Option<String>, StorageError> {
        use wasm_bindgen::JsValue;

        let value = self
            .method("getItem")?
            .call1(&self.storage, &JsValue::from_str(key))
            .map_err(|_| StorageError)?;
        if value.is_null() {
            Ok(None)
        } else {
            value.as_string().map(Some).ok_or(StorageError)
        }
    }

    fn set(&self, key: &str, value: &str) -> Result<(), StorageError> {
        use wasm_bindgen::JsValue;

        self.method("setItem")?
            .call2(
                &self.storage,
                &JsValue::from_str(key),
                &JsValue::from_str(value),
            )
            .map_err(|_| StorageError)?;
        Ok(())
    }

    fn remove(&self, key: &str) -> Result<(), StorageError> {
        use wasm_bindgen::JsValue;

        self.method("removeItem")?
            .call1(&self.storage, &JsValue::from_str(key))
            .map_err(|_| StorageError)?;
        Ok(())
    }
}

#[cfg(target_arch = "wasm32")]
fn browser_logical_day() -> Result<LogicalDay, StorageError> {
    let day = (js_sys::Date::now() / 86_400_000.0).floor();
    if !day.is_finite() || day < 0.0 || day > f64::from(MAX_LOGICAL_DAY) {
        return Err(StorageError);
    }
    Ok(LogicalDay(day as u32))
}

#[cfg(target_arch = "wasm32")]
fn browser_context() -> Result<
    (
        BrowserStorage,
        LogicalDay,
        ParticipantId,
        ConsentEpoch,
        StorageGeneration,
    ),
    StorageError,
> {
    let storage = BrowserStorage::open()?;
    let day = browser_logical_day()?;
    let mut random = [0_u8; 32];
    getrandom::fill(&mut random).map_err(|_| StorageError)?;
    let participant = ParticipantId::parse(&lower_hex(&random[..16])).map_err(|_| StorageError)?;
    let epoch = ConsentEpoch::parse(&lower_hex(&random[16..])).map_err(|_| StorageError)?;
    let generation = browser_generation()?;
    Ok((storage, day, participant, epoch, generation))
}

#[cfg(target_arch = "wasm32")]
fn browser_generation() -> Result<StorageGeneration, StorageError> {
    let mut random = [0_u8; 16];
    getrandom::fill(&mut random).map_err(|_| StorageError)?;
    StorageGeneration::parse(&lower_hex(&random)).map_err(|_| StorageError)
}

#[cfg(target_arch = "wasm32")]
fn lower_hex(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut encoded = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        encoded.push(char::from(HEX[usize::from(byte >> 4)]));
        encoded.push(char::from(HEX[usize::from(byte & 0x0f)]));
    }
    encoded
}

/// Explicit click boundary: this is the first path that may open or read localStorage.
#[cfg(target_arch = "wasm32")]
pub(crate) fn opt_in_and_start() -> EvaluationState {
    let Ok((store, today, participant, epoch, generation)) = browser_context() else {
        return EvaluationState::StorageUnavailable;
    };
    start_with_store(&store, today, participant, epoch, generation)
}

#[cfg(target_arch = "wasm32")]
pub(crate) fn record_due(
    state: &EvaluationState,
    outcome: EvaluationOutcome,
    enjoyment: OptionalRating,
    agency: OptionalRating,
    burden: OptionalRating,
) -> Result<EvaluationState, RecordFailure> {
    let EvaluationState::Active(expected) = state else {
        return Err(RecordFailure::NotActive);
    };
    let store = BrowserStorage::open().map_err(|_| RecordFailure::StorageUnavailable)?;
    let today = browser_logical_day().map_err(|_| RecordFailure::StorageUnavailable)?;
    let generation = browser_generation().map_err(|_| RecordFailure::StorageUnavailable)?;
    record_due_with_store(
        &store, expected, today, generation, outcome, enjoyment, agency, burden,
    )
}

#[cfg(not(target_arch = "wasm32"))]
pub(crate) fn record_due(
    _state: &EvaluationState,
    _outcome: EvaluationOutcome,
    _enjoyment: OptionalRating,
    _agency: OptionalRating,
    _burden: OptionalRating,
) -> Result<EvaluationState, RecordFailure> {
    Err(RecordFailure::StorageUnavailable)
}

#[cfg(not(target_arch = "wasm32"))]
pub(crate) fn opt_in_and_start() -> EvaluationState {
    EvaluationState::StorageUnavailable
}

#[cfg(target_arch = "wasm32")]
pub(crate) fn withdraw() -> EvaluationState {
    let Ok(store) = BrowserStorage::open() else {
        return EvaluationState::StorageUnavailable;
    };
    let Ok(generation) = browser_generation() else {
        return EvaluationState::StorageUnavailable;
    };
    withdraw_with_store(&store, generation)
}

#[cfg(not(target_arch = "wasm32"))]
pub(crate) fn withdraw() -> EvaluationState {
    EvaluationState::StorageUnavailable
}

#[cfg(target_arch = "wasm32")]
pub(crate) fn delete() -> EvaluationState {
    let Ok(store) = BrowserStorage::open() else {
        return EvaluationState::StorageUnavailable;
    };
    let Ok(generation) = browser_generation() else {
        return EvaluationState::StorageUnavailable;
    };
    delete_with_store(&store, generation)
}

#[cfg(not(target_arch = "wasm32"))]
pub(crate) fn delete() -> EvaluationState {
    EvaluationState::StorageUnavailable
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;
    use std::collections::HashMap;

    #[derive(Default)]
    struct MemoryStorage {
        values: RefCell<HashMap<String, String>>,
        reads: RefCell<usize>,
        writes: RefCell<usize>,
    }

    impl EvaluationStorage for MemoryStorage {
        fn get(&self, key: &str) -> Result<Option<String>, StorageError> {
            *self.reads.borrow_mut() += 1;
            Ok(self.values.borrow().get(key).cloned())
        }

        fn set(&self, key: &str, value: &str) -> Result<(), StorageError> {
            *self.writes.borrow_mut() += 1;
            self.values
                .borrow_mut()
                .insert(key.to_owned(), value.to_owned());
            Ok(())
        }

        fn remove(&self, key: &str) -> Result<(), StorageError> {
            *self.writes.borrow_mut() += 1;
            self.values.borrow_mut().remove(key);
            Ok(())
        }
    }

    #[derive(Default)]
    struct DeleteRaceStorage {
        inner: MemoryStorage,
        delete_when_record_claims_fence: RefCell<bool>,
    }

    impl EvaluationStorage for DeleteRaceStorage {
        fn get(&self, key: &str) -> Result<Option<String>, StorageError> {
            self.inner.get(key)
        }

        fn set(&self, key: &str, value: &str) -> Result<(), StorageError> {
            self.inner.set(key, value)?;
            if key == FENCE_STORAGE_KEY
                && value == encode_fence(&second_generation())
                && self.delete_when_record_claims_fence.replace(false)
            {
                let deletion = EvaluationEnvelopeV1 {
                    generation: third_generation(),
                    state: EnvelopeState::Deleted,
                };
                self.inner.values.borrow_mut().insert(
                    FENCE_STORAGE_KEY.to_owned(),
                    encode_fence(&deletion.generation),
                );
                self.inner
                    .values
                    .borrow_mut()
                    .insert(ENVELOPE_STORAGE_KEY.to_owned(), deletion.encode().unwrap());
            }
            Ok(())
        }

        fn remove(&self, key: &str) -> Result<(), StorageError> {
            self.inner.remove(key)
        }
    }

    fn participant() -> ParticipantId {
        ParticipantId::parse("00112233445566778899aabbccddeeff").unwrap()
    }

    fn epoch() -> ConsentEpoch {
        ConsentEpoch::parse("ffeeddccbbaa99887766554433221100").unwrap()
    }

    fn second_epoch() -> ConsentEpoch {
        ConsentEpoch::parse("0123456789abcdef0123456789abcdef").unwrap()
    }

    fn generation() -> StorageGeneration {
        StorageGeneration::parse("11111111111111111111111111111111").unwrap()
    }

    fn second_generation() -> StorageGeneration {
        StorageGeneration::parse("22222222222222222222222222222222").unwrap()
    }

    fn third_generation() -> StorageGeneration {
        StorageGeneration::parse("33333333333333333333333333333333").unwrap()
    }

    fn event(day: u32, timepoint: EvaluationTimepoint) -> EvaluationEvent {
        EvaluationEvent::new(
            LogicalDay(day),
            timepoint,
            ProbeBankVersion::parse(PROBE_BANK_VERSION).unwrap(),
            ProbeId::parse(timepoint.probe_id()).unwrap(),
            EvaluationOutcome::AFirst,
            AsrUsability::Usable,
            OptionalRating::Four,
            OptionalRating::Three,
            OptionalRating::Two,
            PolicyVersion::parse(POLICY_VERSION).unwrap(),
            ScorerVersion::parse(SCORER_VERSION).unwrap(),
            ModelVersion::parse(MODEL_VERSION).unwrap(),
        )
        .unwrap()
    }

    #[test]
    fn failed_asr_accepts_only_unscorable_outcomes() {
        let build = |outcome| {
            EvaluationEvent::new(
                LogicalDay(20_000),
                EvaluationTimepoint::Baseline,
                ProbeBankVersion::parse(PROBE_BANK_VERSION).unwrap(),
                ProbeId::parse(EvaluationTimepoint::Baseline.probe_id()).unwrap(),
                outcome,
                AsrUsability::Failed,
                OptionalRating::Missing,
                OptionalRating::Missing,
                OptionalRating::Missing,
                PolicyVersion::parse(POLICY_VERSION).unwrap(),
                ScorerVersion::parse(SCORER_VERSION).unwrap(),
                ModelVersion::parse(MODEL_VERSION).unwrap(),
            )
        };

        for outcome in [
            EvaluationOutcome::AFirst,
            EvaluationOutcome::ValidAbstention,
            EvaluationOutcome::NotAFirst,
        ] {
            assert_eq!(
                build(outcome),
                Err(RecordError::InvalidCombination),
                "failed ASR accepted {outcome:?}",
            );
        }

        let event = build(EvaluationOutcome::Unscorable)
            .expect("failed ASR must remain recordable as unscorable");
        assert_eq!(event.outcome, EvaluationOutcome::Unscorable);
        assert_eq!(event.asr, AsrUsability::Failed);
    }

    #[test]
    fn no_storage_is_read_or_written_before_explicit_opt_in() {
        let store = MemoryStorage::default();
        let state = EvaluationState::default();

        assert_eq!(state, EvaluationState::Dormant);
        assert_eq!(*store.reads.borrow(), 0);
        assert_eq!(*store.writes.borrow(), 0);
    }

    #[test]
    fn one_probe_per_timepoint_rejects_duplicates() {
        let mut ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        ledger
            .record(
                event(20_000, EvaluationTimepoint::Baseline),
                LogicalDay(20_000),
            )
            .unwrap();

        assert_eq!(
            ledger.record(
                event(20_001, EvaluationTimepoint::Baseline),
                LogicalDay(20_001),
            ),
            Err(RecordError::DuplicateProbeAtTimepoint),
        );
        assert_eq!(ledger.events.len(), 1);
    }

    #[test]
    fn fixed_deadlines_show_only_the_current_unanswered_question() {
        let ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));

        assert!(matches!(
            schedule_for_day(&ledger, LogicalDay(20_000)),
            ScheduleView::Due {
                timepoint: "開始時",
                days_remaining: 3,
                ..
            }
        ));
        assert_eq!(
            schedule_for_day(&ledger, LogicalDay(20_004)),
            ScheduleView::Waiting {
                next_timepoint: "4週目",
                days_until: 21,
                completed: 0,
                missed: 1,
            }
        );
        assert!(matches!(
            schedule_for_day(&ledger, LogicalDay(20_025)),
            ScheduleView::Due {
                timepoint: "4週目",
                days_remaining: 10,
                ..
            }
        ));
        assert!(matches!(
            schedule_for_day(&ledger, LogicalDay(20_143)),
            ScheduleView::Due {
                timepoint: "終了12週後",
                days_remaining: 0,
                ..
            }
        ));
        assert_eq!(
            schedule_for_day(&ledger, LogicalDay(20_144)),
            ScheduleView::Complete {
                completed: 0,
                missed: 5,
            }
        );
    }

    #[test]
    fn observation_view_returns_raw_fixed_categories_in_planned_order() {
        let mut ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        let mut wk4 = event(20_025, EvaluationTimepoint::Wk4);
        wk4.outcome = EvaluationOutcome::ValidAbstention;
        wk4.enjoyment = OptionalRating::Missing;
        wk4.agency = OptionalRating::Five;
        wk4.burden = OptionalRating::One;

        // Insert out of order to ensure the public view follows the planned schedule, not storage
        // order. The view exposes only labels for the finite categories.
        ledger.record(wk4, LogicalDay(20_025)).unwrap();
        ledger
            .record(
                event(20_000, EvaluationTimepoint::Baseline),
                LogicalDay(20_025),
            )
            .unwrap();

        assert_eq!(
            EvaluationState::Active(ledger).observations(),
            vec![
                EvaluationObservationView {
                    timepoint: "開始時",
                    outcome: "結論を先に言えた",
                    enjoyment: "4",
                    agency: "3",
                    burden: "2",
                },
                EvaluationObservationView {
                    timepoint: "4週目",
                    outcome: "分からないと先に言えた",
                    enjoyment: "回答しない",
                    agency: "5",
                    burden: "1",
                },
            ]
        );
        assert!(EvaluationState::Dormant.observations().is_empty());
        assert_eq!(OptionalRating::ALL[0], OptionalRating::Missing);
    }

    #[test]
    fn deadline_and_fixed_probe_identity_fail_closed() {
        let mut ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        assert_eq!(
            ledger.record(
                event(20_004, EvaluationTimepoint::Baseline),
                LogicalDay(20_004),
            ),
            Err(RecordError::OutsideStudyWindow),
        );

        let mut forged = event(20_000, EvaluationTimepoint::Baseline);
        forged.probe_id = ProbeId::parse("forged-probe").unwrap();
        assert_eq!(
            ledger.record(forged, LogicalDay(20_000)),
            Err(RecordError::InvalidDefinition),
        );
        assert!(ledger.events.is_empty());
    }

    #[test]
    fn due_self_report_is_persisted_reloaded_and_duplicate_safe() {
        let store = MemoryStorage::default();
        let started = start_with_store(
            &store,
            LogicalDay(20_000),
            participant(),
            epoch(),
            generation(),
        );
        let EvaluationState::Active(started_ledger) = started else {
            panic!("evaluation did not start");
        };
        assert_eq!(store.values.borrow().len(), 2);
        let fence = store
            .values
            .borrow()
            .get(FENCE_STORAGE_KEY)
            .cloned()
            .unwrap();
        assert!(!fence.contains(participant().as_str()));
        assert!(!fence.contains(epoch().as_str()));

        let recorded = record_due_with_store(
            &store,
            &started_ledger,
            LogicalDay(20_000),
            second_generation(),
            EvaluationOutcome::AFirst,
            OptionalRating::Five,
            OptionalRating::Four,
            OptionalRating::Two,
        )
        .unwrap();
        let EvaluationState::Active(recorded_ledger) = recorded else {
            panic!("evaluation did not remain active");
        };
        assert_eq!(recorded_ledger.events.len(), 1);

        let reloaded = start_with_store(
            &store,
            LogicalDay(20_001),
            participant(),
            second_epoch(),
            third_generation(),
        );
        assert!(matches!(
            reloaded,
            EvaluationState::Active(EvaluationLedgerV1 { ref events, .. }) if events.len() == 1
        ));
        assert_eq!(
            record_due_with_store(
                &store,
                &recorded_ledger,
                LogicalDay(20_001),
                third_generation(),
                EvaluationOutcome::NotAFirst,
                OptionalRating::One,
                OptionalRating::One,
                OptionalRating::Five,
            ),
            Err(RecordFailure::Duplicate),
        );
    }

    #[test]
    fn concurrent_delete_cannot_be_resurrected_by_a_due_record() {
        let store = DeleteRaceStorage::default();
        let started = start_with_store(
            &store,
            LogicalDay(20_000),
            participant(),
            epoch(),
            generation(),
        );
        let EvaluationState::Active(ledger) = started else {
            panic!("evaluation did not start");
        };
        store.delete_when_record_claims_fence.replace(true);

        assert_eq!(
            record_due_with_store(
                &store,
                &ledger,
                LogicalDay(20_000),
                second_generation(),
                EvaluationOutcome::AFirst,
                OptionalRating::Five,
                OptionalRating::Four,
                OptionalRating::Three,
            ),
            Err(RecordFailure::StorageUnavailable),
        );
        let (_, envelope) = read_envelope(&store).unwrap().unwrap();
        assert!(matches!(envelope.state, EnvelopeState::Deleted));
    }

    #[test]
    fn legacy_two_key_state_is_quarantined_and_never_revived() {
        let store = MemoryStorage::default();
        store
            .set(
                LEGACY_CONSENT_STORAGE_KEY,
                "active:ffeeddccbbaa99887766554433221100",
            )
            .unwrap();
        store
            .set(LEGACY_LEDGER_STORAGE_KEY, "legacy-ledger-must-not-migrate")
            .unwrap();

        assert_eq!(
            start_with_store(
                &store,
                LogicalDay(20_000),
                participant(),
                epoch(),
                generation(),
            ),
            EvaluationState::Invalid,
        );
        assert!(!store.values.borrow().contains_key(ENVELOPE_STORAGE_KEY));
        assert_eq!(
            delete_with_store(&store, second_generation()),
            EvaluationState::Deleted,
        );
        assert!(
            !store
                .values
                .borrow()
                .contains_key(LEGACY_LEDGER_STORAGE_KEY)
        );
        let (_, envelope) = read_envelope(&store).unwrap().unwrap();
        assert!(matches!(envelope.state, EnvelopeState::Deleted));
    }

    #[test]
    fn access_time_pruning_keeps_at_least_140_days() {
        let mut ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        ledger
            .record(
                event(20_000, EvaluationTimepoint::Baseline),
                LogicalDay(20_000),
            )
            .unwrap();
        ledger
            .record(event(20_028, EvaluationTimepoint::Wk4), LogicalDay(20_028))
            .unwrap();

        assert!(!ledger.prune(LogicalDay(20_139)));
        assert_eq!(ledger.events.len(), 2);
        assert!(ledger.prune(LogicalDay(20_168)));
        assert_eq!(ledger.events.len(), 1);
        assert_eq!(ledger.events[0].timepoint, EvaluationTimepoint::Wk4);
    }

    #[test]
    fn invalid_oversize_and_unknown_wire_values_fail_closed() {
        assert!(ProbeId::parse("自由記述").is_err());
        assert!(ProbeId::parse("contains spaces").is_err());
        assert!(ProbeId::parse(&"a".repeat(33)).is_err());
        assert_eq!(
            EvaluationLedgerV1::decode(&"x".repeat(MAX_LEDGER_BYTES + 1)),
            Err(DecodeError::TooLarge),
        );

        let ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        let unknown_state = EvaluationEnvelopeV1::active(generation(), ledger.clone())
            .encode()
            .unwrap()
            .replace("\tactive\t", "\texperimental\t");
        assert_eq!(
            EvaluationEnvelopeV1::decode(&unknown_state),
            Err(DecodeError::UnknownEnum),
        );

        let mut with_event = ledger;
        with_event
            .record(
                event(20_000, EvaluationTimepoint::Baseline),
                LogicalDay(20_000),
            )
            .unwrap();
        let invalid_rating = with_event
            .encode()
            .unwrap()
            .replace("\t4\t3\t2\t", "\t6\t3\t2\t");
        assert_eq!(
            EvaluationLedgerV1::decode(&invalid_rating),
            Err(DecodeError::UnknownEnum),
        );
    }

    #[test]
    fn withdrawal_preserves_ledger_and_full_delete_leaves_only_fixed_tombstones() {
        let store = MemoryStorage::default();
        let state = start_with_store(
            &store,
            LogicalDay(20_000),
            participant(),
            epoch(),
            generation(),
        );
        assert!(matches!(state, EvaluationState::Active(_)));
        assert!(store.values.borrow().contains_key(ENVELOPE_STORAGE_KEY));

        assert_eq!(
            withdraw_with_store(&store, second_generation()),
            EvaluationState::Withdrawn
        );
        let withdrawn = store
            .values
            .borrow()
            .get(ENVELOPE_STORAGE_KEY)
            .cloned()
            .unwrap();
        assert!(matches!(
            EvaluationEnvelopeV1::decode(&withdrawn).unwrap().state,
            EnvelopeState::Withdrawn(_)
        ));

        assert_eq!(
            delete_with_store(&store, third_generation()),
            EvaluationState::Deleted,
        );
        let deleted = store
            .values
            .borrow()
            .get(ENVELOPE_STORAGE_KEY)
            .cloned()
            .unwrap();
        assert!(matches!(
            EvaluationEnvelopeV1::decode(&deleted).unwrap().state,
            EnvelopeState::Deleted
        ));
        assert!(!deleted.contains(participant().as_str()));
        assert!(!deleted.contains(epoch().as_str()));
        assert!(
            !store
                .values
                .borrow()
                .contains_key(LEGACY_LEDGER_STORAGE_KEY)
        );
        assert_eq!(
            store.values.borrow().get(LEGACY_CONSENT_STORAGE_KEY),
            Some(&LEGACY_INVALID_MARKER.to_owned())
        );
    }

    #[test]
    fn serialized_schema_has_no_free_text_or_sensitive_payload_slots() {
        let mut ledger = EvaluationLedgerV1::new(participant(), epoch(), LogicalDay(20_000));
        ledger
            .record(
                event(20_000, EvaluationTimepoint::Baseline),
                LogicalDay(20_000),
            )
            .unwrap();
        let encoded = ledger.encode().unwrap();

        for forbidden in [
            "audio",
            "transcript",
            "free_text",
            "firebase_uid",
            "email",
            "timestamp_ms",
            "latency",
            "active_control",
            "gentle",
            "これは秘密です",
        ] {
            assert!(!encoded.contains(forbidden), "unexpected slot: {forbidden}");
        }
        for timepoint in EvaluationTimepoint::ALL {
            assert!(!encoded.contains(timepoint.question()));
        }
        assert_eq!(EvaluationLedgerV1::decode(&encoded), Ok(ledger));
    }
}
