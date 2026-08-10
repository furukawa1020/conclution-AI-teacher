mod longitudinal;

use dioxus::prelude::*;
use serde::Deserialize;

const PRODUCT_PROMISE_COPY: &str = "AIが話すより、あなたが話せるために";
const ORDINARY_CHAT_COPY: &str = "「こんにちは」だけで、次の一言を一緒に見つける";
const ANSWER_SUPPORT_COPY: &str =
    "「一問だけ手伝って」で、本人のAを確認できたらAIが黙って発話権を返す";
const TALK_ONLY_COPY: &str = "届いた瞬間だけ知らせて、点数にはしない";
const STANDARD_MODE_ROUTE_LABEL: &str =
    "通常会話はNative Audio / 明示した回答支援はQ-ARC + QBA Proof";
const STANDARD_MODE_ROUTE_COPY: &str = "ライブ会話では原音をCloud Runからus-central1のVertex AI Native Audioへ送り、通常は音声を直接返します。人に聞かれた質問への回答支援を本人が明示した時はNative生成音声を破棄し、確定入力字幕を再認識せず監査済みcontrollerへ直接渡します。発話途中に同じ字幕が安定した時は、答え本文を入力できないQ-ARCが質問の型だけの短いcueとTTSを非公開buffer内で先に準備します。Nativeの最終字幕との完全一致、browser commit、同一requestと回答scopeへ暗号学的に束縛した有限checkpointがそろった後だけ音声とstateを解放します。続く回答発話もNativeの確定入力字幕を再認識せずcontrollerへ渡し、exact-span gateと独立criticの一致後に、待つ・型だけ促す・一度だけ言い直しを頼む・完了・解放を選びます。AIはAを作らず、本人自身のAが先に出た時だけ本文を含まないQBA Proofを返します。外部で質問された事実、話者、ライブネス、正解、能力、上達は確認しません。Native接続不能時だけ東京リージョンSTTを一度使う段階経路へ切り替えます。PDF入力は公開版では推論前に拒否します。";
const STANDARD_VOICE_PRIVACY_COPY: &str = "ライブ発話はTLSでCloud Runからus-central1のVertex AI Native Audioへ原音を送ります。回答支援中もNativeの確定入力字幕を同じ原音の再認識なしで監査済みcontrollerへ直接渡します。途中候補から準備した判断と音声は、最終字幕との完全一致、browser commit、request・回答scope拘束済みcheckpointまで外へ出しません。短期stateにはoperator・required slot・非可逆tagと本文を含まない固定長posteriorだけを保持し、具体的な質問・答え・文字起こし・診断は保存しません。AIは本人より先にAを言いません。QBA Proofも固定enumだけで、外部質問の事実、話者、ライブネス、正解、能力、上達は確認しません。Native接続不能時は東京リージョンSTTを一度使い、PDF入力は公開版の全モードで推論前に拒否します。原音・本文はKOTAEの会話履歴、Firestore、Cloud Storage、アプリログへ保存しません。";
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
const COACH_CHECKPOINT_MAX_CHARS: usize = 16 * 1024;
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
const NATIVE_RESPONDENT_COACH_ROUTE: &str = "native-respondent-coach";
const RETURNING_PASSKEY_ACTION: &str = "登録済みの方　同じパスキーで戻る";
const NEW_PASSKEY_ACCOUNT_ACTION: &str = "初めての方　新しい仮名アカウントを作る";
const SEPARATE_PASSKEY_ACCOUNT_WARNING: &str =
    "この登録は既存の仮名アカウントとは別のアカウントを作ります。認証失敗から自動登録はしません。";
const PASSKEY_REQUIRED_COPY: &str =
    "話し始めるを押して　パスキーでアカウント操作を確認してください";
const PASSKEY_CANCELLED_COPY: &str = "パスキーで戻る操作は完了しませんでした　戻る場合はもう一度「登録済みの方　同じパスキーで戻る」を選んでください　マイクは開いていません";
const PASSKEY_UNSUPPORTED_COPY: &str =
    "このブラウザではパスキーを確認できません　マイクは開いていません";
const PASSKEY_AUTHENTICATION_FAILED_COPY: &str = "このパスキーでは戻れませんでした　初めて使う方や登録が未完了の方は「新しい仮名アカウントを作る」を選んでください　マイクは開いていません";
const PASSKEY_REGISTRATION_CANCELLED_COPY: &str = "新しいパスキーの登録は完了しませんでした　登録する場合はもう一度「新しい仮名アカウントを作る」を選んでください　マイクは開いていません";
const PASSKEY_REGISTRATION_FAILED_COPY: &str = "新しいパスキーを登録できませんでした　端末のパスキー設定を確認してもう一度ためしてください　マイクは開いていません";
const PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY: &str = "登録結果を安全に確認できませんでした　新規登録を繰り返さず「登録済みの方　同じパスキーで戻る」を選び　いま作成したパスキーを使ってください　マイクは開いていません";
const PASSKEY_REGISTRATION_SUCCESS_COPY: &str =
    "新しい仮名アカウントを作りました　マイクはまだ開いていません";
const PASSKEY_ACCOUNT_EXISTS_COPY: &str =
    "このタブには既存アカウントがあります　新しい別アカウントは作りませんでした";
const ACCOUNT_BOUNDARY_CHANGED_COPY: &str =
    "別の仮名アカウントへ切り替わったため　前の会話を閉じました　もう一度話し始めてください";
const SUPPORT_BOUNDARY_COPY: &str = "話題を用意できない時は「こんにちは」だけでKOTAEが短い話題を持ち、短い返事・相づち・まとまらない長話を失敗扱いしません。AIが長く話すより利用者の次の一言を優先し、訓練や採点を前面に出さず、返事の中で中心を先に受け取れる形へ整えます。外出・学校・仕事・家族への相談を勝手に目標にしません。通常の受領表示は声が届いたことだけを示します。明示回答支援では、入力内に報告された問い、確定した今回の入力発話、全required slot、A先頭を二重検証できた時だけ、本文を含まないQBA Proofを表示します。話者、ライブネス、外部で実際にその問いを聞かれた事実、正解、能力、上達、他場面への転移は判定しません。";
const STRICT_PRIVACY_BLOCKED_COPY: &str = "個人情報の可能性があるため、この発話はAIへ進めませんでした。言い直さなくて大丈夫です。厳格モードを切り替えるか、別の話題から続けられます。";

#[derive(Clone, Copy, PartialEq, Eq)]
enum PasskeySetupFeedback {
    Success(&'static str),
    Error(&'static str),
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum VoiceState {
    Ready,
    RequestingPermission,
    Listening,
    Thinking,
    Speaking,
    Paused,
    Error(&'static str),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum VoiceReceipt {
    Clear,
    Received,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum TurnNotice {
    Clear,
    CaptureSkipped,
    ReplyUnavailable,
    PrivacyBlocked,
}

impl TurnNotice {
    const fn is_visible(self) -> bool {
        !matches!(self, Self::Clear)
    }

    const fn heading(self) -> &'static str {
        match self {
            Self::Clear => "",
            Self::CaptureSkipped => "こちらで声を受け切れませんでした",
            Self::ReplyUnavailable => "こちらの返事だけ止まりました",
            Self::PrivacyBlocked => "この発話はAIへ進めませんでした",
        }
    }

    const fn hint(self) -> &'static str {
        match self {
            Self::Clear => "",
            Self::CaptureSkipped => {
                "会話は開いたままです。あなたの言い方の問題ではありません。別の一言から続けられます"
            }
            Self::ReplyUnavailable => {
                "声は言い直さなくて大丈夫です。会話は開いたままなので、そのまま続けられます"
            }
            Self::PrivacyBlocked => {
                "言い直さなくて大丈夫です。厳格モードを切るか、別の話題から続けられます"
            }
        }
    }
}

impl VoiceReceipt {
    const fn is_visible_for(self, state: VoiceState) -> bool {
        matches!(self, Self::Received)
            && matches!(state, VoiceState::Listening | VoiceState::Thinking)
    }

    const fn eyebrow(self) -> &'static str {
        "声の受け取り"
    }

    const fn heading(self) -> &'static str {
        "ここまで届いています"
    }

    const fn hint(self, state: VoiceState) -> &'static str {
        match state {
            VoiceState::Listening => "まだ続けても大丈夫　話し始めればそのまま聴き続けます",
            VoiceState::Thinking => "返事を整えています　言い直さなくて大丈夫です",
            _ => "",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct VoiceStartLatency {
    milliseconds: u32,
}

impl VoiceStartLatency {
    const ONE_SECOND_GOAL_MS: u32 = 1_000;
    const MAXIMUM_EVENT_MS: f64 = 120_000.0;

    const fn is_on_target(self) -> bool {
        self.milliseconds <= Self::ONE_SECOND_GOAL_MS
    }

    fn status(self) -> String {
        let seconds = f64::from(self.milliseconds) / 1_000.0;
        if self.is_on_target() {
            format!("返答開始 約{seconds:.1}秒 / 1秒目標内")
        } else {
            format!("返答開始 約{seconds:.1}秒 / さらに短縮中")
        }
    }

    const fn class_name(self) -> &'static str {
        if self.is_on_target() {
            "voice-status__latency is-on-target"
        } else {
            "voice-status__latency"
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum EvaluationStep {
    Prompt,
    SelfReport,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum VoiceTurnMode {
    Intentional,
    Foreground,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
enum CoachPhase {
    None,
    AwaitingAnswer,
    AwaitingRestatement,
    Expanding,
    Complete,
    Blocked,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
enum CoachAction {
    None,
    Elicit,
    Restate,
    Expand,
    Complete,
    Retry,
    Release,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
enum AnswerProof {
    #[default]
    None,
    QuestionBoundInputAnswerFirst,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct CoachState {
    phase: CoachPhase,
    action: CoachAction,
    answer_proof: AnswerProof,
}

impl CoachState {
    const NONE: Self = Self {
        phase: CoachPhase::None,
        action: CoachAction::None,
        answer_proof: AnswerProof::None,
    };

    const fn from_result(phase: CoachPhase, action: CoachAction) -> Self {
        Self {
            phase,
            action,
            answer_proof: AnswerProof::None,
        }
    }

    const fn from_authoritative_result(
        phase: CoachPhase,
        action: CoachAction,
        answer_proof: AnswerProof,
    ) -> Self {
        Self {
            phase,
            action,
            answer_proof,
        }
    }

    const fn without_answer_proof(self) -> Self {
        Self {
            phase: self.phase,
            action: self.action,
            answer_proof: AnswerProof::None,
        }
    }

    const fn is_active(self) -> bool {
        !matches!(self.phase, CoachPhase::None)
    }

    const fn requires_staged_route(self) -> bool {
        matches!(
            (self.phase, self.action),
            (CoachPhase::AwaitingAnswer, CoachAction::Elicit)
                | (CoachPhase::AwaitingRestatement, CoachAction::Restate)
                | (CoachPhase::Expanding, CoachAction::Expand)
                | (CoachPhase::Blocked, CoachAction::Retry)
        )
    }

    const fn yielded_after_owned_answer(self) -> bool {
        matches!(
            (self.answer_proof, self.phase, self.action),
            (
                AnswerProof::QuestionBoundInputAnswerFirst,
                CoachPhase::Complete,
                CoachAction::Complete
            )
        )
    }

    const fn status(self) -> &'static str {
        if self.yielded_after_owned_answer() {
            return "回答所有権 / AI発話なし";
        }
        if matches!(
            self.answer_proof,
            AnswerProof::QuestionBoundInputAnswerFirst
        ) {
            return "今回の入力 / A先頭確認";
        }
        match self.action {
            CoachAction::Elicit => "あなたの一言を待っています",
            CoachAction::Restate => "あなたの言葉をそのまま",
            CoachAction::Expand => "あなたの続きを待っています",
            CoachAction::Complete => "あなたの言葉を受け取りました",
            CoachAction::Retry => "急がせず待っています",
            CoachAction::Release => "話したい方へ戻れます",
            CoachAction::None => match self.phase {
                CoachPhase::None => "",
                CoachPhase::AwaitingAnswer => "あなたの一言を待っています",
                CoachPhase::AwaitingRestatement => "あなたの言葉をそのまま",
                CoachPhase::Expanding => "あなたの続きを待っています",
                CoachPhase::Complete => "あなたの言葉を受け取りました",
                CoachPhase::Blocked => "そのままで大丈夫",
            },
        }
    }

    const fn heading(self) -> &'static str {
        if self.yielded_after_owned_answer() {
            return "あなたのAが先に出たので、AIは黙りました";
        }
        if matches!(
            self.answer_proof,
            AnswerProof::QuestionBoundInputAnswerFirst
        ) {
            return "報告された問いへの入力が、Aから始まりました";
        }
        match (self.phase, self.action) {
            (CoachPhase::Blocked, CoachAction::Release) => "そのまま話して大丈夫です",
            (CoachPhase::None, _) => "まとまらないまま、話していい",
            (CoachPhase::AwaitingAnswer, _) => "今は、あなたの一言だけで大丈夫",
            (CoachPhase::AwaitingRestatement, _) => "そこまで、ちゃんと聞こえています",
            (CoachPhase::Expanding, _) => "話したい続きを、一つだけ",
            (CoachPhase::Complete, _) => "あなた自身の言葉が出ました",
            (CoachPhase::Blocked, _) => "急がなくて大丈夫です",
        }
    }

    const fn hint(self) -> &'static str {
        if self.yielded_after_owned_answer() {
            return "今回の入力だけを報告された問いへ束縛し、A先頭を二重確認しました。KOTAEは答えも相づちも足さず、ここで発話権を返しました。話者・ライブネス・外部で実際にその問いを聞かれた事実・正解・能力・上達は確認していません";
        }
        if matches!(
            self.answer_proof,
            AnswerProof::QuestionBoundInputAnswerFirst
        ) {
            return "KOTAEが答えを補った入力ではありません。今回の入力内でA先頭を確認しただけです。話者・ライブネス・外部で実際にその問いを聞かれた事実・正解・能力・上達は確認していません";
        }
        match (self.phase, self.action) {
            (CoachPhase::Blocked, CoachAction::Release) => "この続きでも、別の話でも大丈夫",
            (CoachPhase::None, _) => "小さな声でも、3分ほどまとまらなくても、そのままどうぞ",
            (CoachPhase::AwaitingAnswer, _) => "わからない、まだ決めていない、でも答えになります",
            (CoachPhase::AwaitingRestatement, _) => "聞きたいところを一つだけ小さくしています",
            (CoachPhase::Expanding, _) => "答えなくても大丈夫　話したい方へ続けられます",
            (CoachPhase::Complete, _) => {
                "今の一回で声に出せたことだけの表示です　上達や長期の変化を表すものではありません"
            }
            (CoachPhase::Blocked, _) => "短くても大丈夫　拾えなければそのまま先へ進めます",
        }
    }
}

impl VoiceTurnMode {
    #[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
    const fn as_str(self) -> &'static str {
        match self {
            Self::Intentional => "intentional",
            Self::Foreground => "foreground",
        }
    }
}

impl VoiceState {
    const fn label(self) -> &'static str {
        match self {
            Self::Ready => "話しはじめる",
            Self::RequestingPermission => "マイクを準備しています",
            Self::Listening => "あなたの言葉を待っています",
            Self::Thinking => "短い返事を整えています",
            Self::Speaking => "短く返しています",
            Self::Paused => "会話を止めています",
            Self::Error(message) => message,
        }
    }

    const fn eyebrow(self) -> &'static str {
        match self {
            Self::Ready => "あなたが話すための音声AI",
            Self::Listening => "あなたの番",
            Self::Thinking => "短い返事を準備",
            Self::Speaking => "KOTAEの番・短く返す",
            Self::Error(_) => "接続を続けられませんでした",
            _ => self.label(),
        }
    }

    const fn hint(self) -> &'static str {
        match self {
            Self::Ready => {
                "うまく話す準備はいりません。「こんにちは」、一言、沈黙、聞くだけから。KOTAEは短く返して、次の言葉を待ちます"
            }
            Self::RequestingPermission => "この会話に使うマイクを選ぶ",
            Self::Listening => "話し終わりの間を見て自動で返す　急ぐ時は「ここで返して」を選べます",
            Self::Thinking => "答えを奪わず、次の言葉につながる一つだけを返します",
            Self::Speaking => "ぼやきや相づちはそのままで大丈夫　返事を止めたい時は少し続けて話す",
            Self::Paused => "マイクは止まってる　再開まで何も取り込まない",
            Self::Error(_) => "丸いボタンか下の「もう一度接続する」からやり直せる",
        }
    }

    const fn active_heading(self) -> &'static str {
        match self {
            Self::Listening => "あなたの言葉を、急がず待っています",
            Self::Thinking => "言いたかったことを、短く受け取ります",
            Self::Speaking => "短く返したら、またあなたの番です",
            _ => "まとまらないまま、話していい",
        }
    }

    const fn active_hint(self) -> &'static str {
        match self {
            Self::Listening => "一言でも、途中でも、沈黙のあとでも大丈夫",
            Self::Thinking => "長い講評や点数にはしません",
            Self::Speaking => "聞くだけでも、話したくなったら続けても大丈夫",
            _ => self.hint(),
        }
    }

    const fn class_name(self) -> &'static str {
        match self {
            Self::Ready => "is-ready",
            Self::RequestingPermission => "is-requesting",
            Self::Listening => "is-listening",
            Self::Thinking => "is-thinking",
            Self::Speaking => "is-speaking",
            Self::Paused => "is-paused",
            Self::Error(_) => "is-error",
        }
    }

    const fn orb_action(self) -> &'static str {
        match self {
            Self::Ready => "話しはじめる",
            Self::Listening => "自動で聴き取り中",
            Self::Paused => "一時停止中",
            Self::Error(_) => "もう一度接続する",
            Self::RequestingPermission | Self::Thinking | Self::Speaking => "処理中",
        }
    }

    const fn orb_disabled(self) -> bool {
        !matches!(self, Self::Ready | Self::Error(_))
    }

    const fn session_active(self) -> bool {
        !matches!(self, Self::Ready)
    }

    const fn session_control_reconnects(self) -> bool {
        matches!(self, Self::Paused | Self::Error(_))
    }

    const fn session_control_takes_turn(self) -> bool {
        matches!(self, Self::Thinking | Self::Speaking)
    }

    const fn session_control_label(self) -> &'static str {
        match self {
            Self::Paused => "再開",
            Self::Error(_) => "もう一度接続する",
            Self::Thinking | Self::Speaking => "今話す",
            _ => "一時停止",
        }
    }

    const fn session_control_icon(self) -> &'static str {
        match self {
            Self::Paused => "▶",
            Self::Error(_) => "↻",
            Self::Thinking | Self::Speaking => "●",
            _ => "Ⅱ",
        }
    }
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CloudState {
    Connecting,
    Ready,
    IdentityRequired,
    PasskeyRequired,
    PasskeyRegistrationRecoveryRequired,
    ConfigurationRequired,
    Unavailable,
}

impl CloudState {
    const fn label(self) -> &'static str {
        match self {
            Self::Connecting => "SECURE LINK / …",
            Self::Ready => "SECURE LINK / READY",
            Self::IdentityRequired => "ACCOUNT / REQUIRED",
            Self::PasskeyRequired => "PASSKEY / REQUIRED",
            Self::PasskeyRegistrationRecoveryRequired => "PASSKEY / RECOVERY",
            Self::ConfigurationRequired => "SECURE LINK / SETUP",
            Self::Unavailable => "SECURE LINK / OFFLINE",
        }
    }

    const fn class_name(self) -> &'static str {
        match self {
            Self::Connecting
            | Self::IdentityRequired
            | Self::PasskeyRequired
            | Self::PasskeyRegistrationRecoveryRequired
            | Self::ConfigurationRequired => "cloud-pill is-pending",
            Self::Ready => "cloud-pill is-ready",
            Self::Unavailable => "cloud-pill is-offline",
        }
    }
}

#[derive(Clone, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
struct VoiceTurnResult {
    audio_base64: String,
    audio_mime_type: String,
    streamed_audio: bool,
    #[serde(default)]
    interrupted: bool,
    session_state: String,
    detected_domain: String,
    assistance_target: String,
    respondent_stage: String,
    coach_phase: CoachPhase,
    coach_action: CoachAction,
    #[serde(default)]
    answer_proof: AnswerProof,
    route: String,
    needs_paper: bool,
    research_status: ResearchStatus,
    research_records: Vec<ResearchRecord>,
    #[serde(default)]
    privacy_status: String,
    #[serde(default)]
    caption: Option<String>,
}

#[derive(Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
enum ResearchStatus {
    None,
    NeedsPrimaryEvidence,
    Unavailable,
}

#[derive(Clone, PartialEq, Deserialize)]
struct ResearchRecord {
    title: String,
    doi: String,
    url: String,
    published: String,
    source: String,
}

impl ResearchRecord {
    fn display_title(&self) -> &str {
        if self.title.is_empty() {
            "タイトル未収録"
        } else {
            &self.title
        }
    }
}

#[derive(Clone, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
struct DocumentInfo {
    name: String,
    size_bytes: u64,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct BridgeStatus {
    state: String,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct TurnEnd {
    has_speech: bool,
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
enum FinishTurnError {
    Interrupted,
    Recoverable(&'static str),
    Message(&'static str),
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
enum WaitTurnError {
    Recoverable(&'static str),
    Terminal(&'static str),
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn recoverable_wait_turn_code(code: Option<&str>) -> bool {
    matches!(code, Some("voice_turn_too_large"))
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn recoverable_finish_turn_code(code: Option<&str>) -> bool {
    matches!(
        code,
        Some(
            "no_speech"
                | "rate_limited"
                | "voice_api_unavailable"
                | "voice_turn_too_large"
                | "voice_turn_timeout"
                | "voice_turn_unavailable"
        )
    )
}

#[cfg(target_arch = "wasm32")]
mod cloud {
    use super::{
        ACCOUNT_BOUNDARY_CHANGED_COPY, BridgeStatus, CloudState, CoachState, DocumentInfo,
        FinishTurnError, PASSKEY_ACCOUNT_EXISTS_COPY, PASSKEY_AUTHENTICATION_FAILED_COPY,
        PASSKEY_CANCELLED_COPY, PASSKEY_REGISTRATION_CANCELLED_COPY,
        PASSKEY_REGISTRATION_FAILED_COPY, PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY,
        PASSKEY_REQUIRED_COPY, PASSKEY_UNSUPPORTED_COPY, PasskeySetupFeedback, ResearchRecord,
        ResearchStatus, STRICT_PRIVACY_BLOCKED_COPY, TurnEnd, VoiceReceipt, VoiceState,
        VoiceTurnMode, VoiceTurnResult, WaitTurnError, coach_action_from_checkpoint,
        coach_phase_from_checkpoint, confirmed_voice_input_state, recoverable_finish_turn_code,
        recoverable_wait_turn_code, session_stop_pauses, valid_coach_checkpoint_keys,
        valid_coach_checkpoint_metadata, valid_voice_pause_metadata, valid_voice_receipt_metadata,
        validated_voice_start_latency,
    };
    use dioxus::prelude::{ReadableExt, Signal, WritableExt};
    use std::rc::Rc;
    use wasm_bindgen::JsCast;
    use wasm_bindgen::prelude::*;

    pub(super) struct DocumentClearListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    impl Drop for DocumentClearListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:document-cleared",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    pub(super) struct AccountAccessRefreshListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    pub(super) struct AccountBoundaryChangedListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    impl Drop for AccountAccessRefreshListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:account-access-confirmed",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    impl Drop for AccountBoundaryChangedListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:account-boundary-changed",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    pub(super) struct FirstAudioListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    pub(super) struct CoachCheckpointListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    pub(super) struct VoiceReceiptListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    pub(super) struct VoiceInputConfirmedListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    pub(super) struct VoiceStartLatencyListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    impl Drop for FirstAudioListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:first-audio",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    impl Drop for CoachCheckpointListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:coach-checkpoint",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    impl Drop for VoiceReceiptListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:voice-receipt",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    impl Drop for VoiceInputConfirmedListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:voice-input-confirmed",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    impl Drop for VoiceStartLatencyListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:voice-start-latency",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    pub(super) struct VoiceInterruptedListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    impl Drop for VoiceInterruptedListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:voice-interrupted",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    pub(super) struct VoiceSessionPausedListener {
        window: web_sys::Window,
        callback: Closure<dyn FnMut(web_sys::Event)>,
    }

    impl Drop for VoiceSessionPausedListener {
        fn drop(&mut self) {
            let _ = self.window.remove_event_listener_with_callback(
                "kotae:voice-session-paused",
                self.callback.as_ref().unchecked_ref(),
            );
        }
    }

    #[wasm_bindgen]
    extern "C" {
        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = getStatus)]
        async fn get_status_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = registerPasskeyAccount)]
        async fn register_passkey_account_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = beginTurn)]
        async fn begin_turn_js(
            session_state: &str,
            turn_mode: &str,
            strict_cloud_minimization: bool,
            coach_active: bool,
        ) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = waitForTurnEnd)]
        async fn wait_for_turn_end_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = endTurn)]
        fn end_turn_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = finishTurn)]
        async fn finish_turn_js(
            session_state: &str,
            turn_mode: &str,
            strict_cloud_minimization: bool,
        ) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = stopSession)]
        fn stop_session_js() -> Result<(), JsValue>;
    }

    pub async fn status() -> CloudState {
        let Ok(value) = get_status_js().await else {
            return CloudState::Unavailable;
        };
        let Ok(status) = serde_wasm_bindgen::from_value::<BridgeStatus>(value) else {
            return CloudState::Unavailable;
        };
        match status.state.as_str() {
            "ready" => CloudState::Ready,
            "identity-required" => CloudState::IdentityRequired,
            "passkey-required" => CloudState::PasskeyRequired,
            "passkey-registration-recovery-required" => {
                CloudState::PasskeyRegistrationRecoveryRequired
            }
            "configuration-required" => CloudState::ConfigurationRequired,
            _ => CloudState::Unavailable,
        }
    }

    pub async fn register_passkey_account() -> Result<(), &'static str> {
        register_passkey_account_js()
            .await
            .map(|_| ())
            .map_err(user_message)
    }

    pub async fn begin_turn(
        session_state: &str,
        turn_mode: VoiceTurnMode,
        strict_cloud_minimization: bool,
        coach_active: bool,
    ) -> Result<(), &'static str> {
        begin_turn_js(
            session_state,
            turn_mode.as_str(),
            strict_cloud_minimization,
            coach_active,
        )
        .await
        .map(|_| ())
        .map_err(user_message)
    }

    pub async fn wait_for_turn_end() -> Result<bool, WaitTurnError> {
        let value = match wait_for_turn_end_js().await {
            Ok(value) => value,
            Err(error) => {
                let code = error_code(error.clone());
                let message = user_message(error);
                if recoverable_wait_turn_code(code.as_deref()) {
                    return Err(WaitTurnError::Recoverable(message));
                }
                return Err(WaitTurnError::Terminal(message));
            }
        };
        serde_wasm_bindgen::from_value::<TurnEnd>(value)
            .map(|result| result.has_speech)
            .map_err(|_| WaitTurnError::Terminal("マイクの状態を確認できない"))
    }

    pub async fn finish_turn(
        session_state: &str,
        turn_mode: VoiceTurnMode,
        strict_cloud_minimization: bool,
    ) -> Result<VoiceTurnResult, FinishTurnError> {
        let value = match finish_turn_js(
            session_state,
            turn_mode.as_str(),
            strict_cloud_minimization,
        )
        .await
        {
            Ok(value) => value,
            Err(error) if error_code(error.clone()).as_deref() == Some("voice_interrupted") => {
                return Err(FinishTurnError::Interrupted);
            }
            Err(error) => {
                let code = error_code(error.clone());
                let message = user_message(error);
                if recoverable_finish_turn_code(code.as_deref()) {
                    return Err(FinishTurnError::Recoverable(message));
                }
                return Err(FinishTurnError::Message(message));
            }
        };
        serde_wasm_bindgen::from_value(value)
            .map_err(|_| FinishTurnError::Message("音声応答を確認できない　もう一度ためしてみて"))
    }

    pub fn install_document_clear_listener(
        mut document_info: Signal<Option<DocumentInfo>>,
    ) -> Option<Rc<DocumentClearListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
            document_info.set(None);
        });
        window
            .add_event_listener_with_callback(
                "kotae:document-cleared",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(DocumentClearListener { window, callback }))
    }

    pub fn install_account_access_refresh_listener(
        mut cloud_status_refresh: Signal<u64>,
    ) -> Option<Rc<AccountAccessRefreshListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
            let next = cloud_status_refresh.peek().wrapping_add(1);
            cloud_status_refresh.set(next);
        });
        window
            .add_event_listener_with_callback(
                "kotae:account-access-confirmed",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(AccountAccessRefreshListener { window, callback }))
    }

    #[allow(clippy::too_many_arguments)]
    pub fn install_account_boundary_changed_listener(
        mut voice_state: Signal<VoiceState>,
        mut generation: Signal<u64>,
        mut session_state: Signal<String>,
        mut detected_domain: Signal<String>,
        mut route: Signal<String>,
        mut coach_state: Signal<CoachState>,
        mut needs_paper: Signal<bool>,
        mut research_status: Signal<ResearchStatus>,
        mut research_records: Signal<Vec<ResearchRecord>>,
        mut document_info: Signal<Option<DocumentInfo>>,
        mut document_error: Signal<Option<&'static str>>,
        mut caption: Signal<Option<String>>,
        mut passkey_setup_feedback: Signal<Option<PasskeySetupFeedback>>,
        mut cloud_status_refresh: Signal<u64>,
    ) -> Option<Rc<AccountBoundaryChangedListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
            let next = generation.peek().wrapping_add(1);
            generation.set(next);
            let _ = stop_session_js();
            session_state.set(String::new());
            detected_domain.set(String::new());
            route.set(String::new());
            coach_state.set(CoachState::NONE);
            needs_paper.set(false);
            research_status.set(ResearchStatus::None);
            research_records.set(Vec::new());
            document_info.set(None);
            document_error.set(None);
            caption.set(None);
            passkey_setup_feedback.set(None);
            let next_refresh = cloud_status_refresh.peek().wrapping_add(1);
            cloud_status_refresh.set(next_refresh);
            voice_state.set(VoiceState::Error(ACCOUNT_BOUNDARY_CHANGED_COPY));
        });
        window
            .add_event_listener_with_callback(
                "kotae:account-boundary-changed",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(AccountBoundaryChangedListener { window, callback }))
    }

    pub fn focus_element(element_id: &str) {
        let Some(document) = web_sys::window().and_then(|window| window.document()) else {
            return;
        };
        let Some(element) = document.get_element_by_id(element_id) else {
            return;
        };
        let Ok(element) = element.dyn_into::<web_sys::HtmlElement>() else {
            return;
        };
        let _ = element.focus();
    }

    pub fn install_voice_receipt_listener(
        mut voice_receipt: Signal<VoiceReceipt>,
    ) -> Option<Rc<VoiceReceiptListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |event: web_sys::Event| {
            let event_value = event.as_ref();
            let Ok(detail) = js_sys::Reflect::get(event_value, &JsValue::from_str("detail")) else {
                return;
            };
            let Some(detail_object) = detail.dyn_ref::<js_sys::Object>() else {
                return;
            };
            let keys = js_sys::Object::keys(detail_object);
            let Ok(phase) = js_sys::Reflect::get(&detail, &JsValue::from_str("phase")) else {
                return;
            };
            let Ok(version) = js_sys::Reflect::get(&detail, &JsValue::from_str("version")) else {
                return;
            };
            let Some(phase) = phase.as_string() else {
                return;
            };
            let Some(version) = version.as_f64() else {
                return;
            };
            if !valid_voice_receipt_metadata(&phase, version, keys.length()) {
                return;
            }
            voice_receipt.set(if phase == "received" {
                VoiceReceipt::Received
            } else {
                VoiceReceipt::Clear
            });
        });
        window
            .add_event_listener_with_callback(
                "kotae:voice-receipt",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(VoiceReceiptListener { window, callback }))
    }

    pub fn install_voice_input_confirmed_listener(
        mut coach_state: Signal<CoachState>,
    ) -> Option<Rc<VoiceInputConfirmedListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |event: web_sys::Event| {
            let event_value = event.as_ref();
            let Ok(detail) = js_sys::Reflect::get(event_value, &JsValue::from_str("detail")) else {
                return;
            };
            let Some(detail_object) = detail.dyn_ref::<js_sys::Object>() else {
                return;
            };
            let keys = js_sys::Object::keys(detail_object);
            let Some(key_names) = keys
                .iter()
                .map(|key| key.as_string())
                .collect::<Option<Vec<_>>>()
            else {
                return;
            };
            let Ok(version) = js_sys::Reflect::get(&detail, &JsValue::from_str("version")) else {
                return;
            };
            let Some(version) = version.as_f64() else {
                return;
            };
            let Some(next_state) =
                confirmed_voice_input_state(*coach_state.peek(), version, &key_names)
            else {
                return;
            };
            coach_state.set(next_state);
        });
        window
            .add_event_listener_with_callback(
                "kotae:voice-input-confirmed",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(VoiceInputConfirmedListener { window, callback }))
    }

    pub fn install_voice_start_latency_listener(
        mut voice_start_latency: Signal<Option<super::VoiceStartLatency>>,
    ) -> Option<Rc<VoiceStartLatencyListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |event: web_sys::Event| {
            let event_value = event.as_ref();
            let Ok(detail) = js_sys::Reflect::get(event_value, &JsValue::from_str("detail")) else {
                return;
            };
            let Some(detail_object) = detail.dyn_ref::<js_sys::Object>() else {
                return;
            };
            let keys = js_sys::Object::keys(detail_object);
            let Some(key_names) = keys
                .iter()
                .map(|key| key.as_string())
                .collect::<Option<Vec<_>>>()
            else {
                return;
            };
            let Ok(version) = js_sys::Reflect::get(&detail, &JsValue::from_str("version")) else {
                return;
            };
            let Some(version) = version.as_f64() else {
                return;
            };
            let Ok(milliseconds) =
                js_sys::Reflect::get(&detail, &JsValue::from_str("milliseconds"))
            else {
                return;
            };
            let milliseconds = if milliseconds.is_null() {
                None
            } else {
                let Some(value) = milliseconds.as_f64() else {
                    return;
                };
                Some(value)
            };
            let Some(next_latency) =
                validated_voice_start_latency(milliseconds, version, &key_names)
            else {
                return;
            };
            voice_start_latency.set(next_latency);
        });
        window
            .add_event_listener_with_callback(
                "kotae:voice-start-latency",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(VoiceStartLatencyListener { window, callback }))
    }

    pub fn install_first_audio_listener(
        mut voice_state: Signal<VoiceState>,
        mut voice_receipt: Signal<VoiceReceipt>,
    ) -> Option<Rc<FirstAudioListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
            voice_receipt.set(VoiceReceipt::Clear);
            if *voice_state.peek() == VoiceState::Thinking {
                voice_state.set(VoiceState::Speaking);
            }
        });
        window
            .add_event_listener_with_callback(
                "kotae:first-audio",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(FirstAudioListener { window, callback }))
    }

    pub fn install_coach_checkpoint_listener(
        mut session_state: Signal<String>,
        mut route: Signal<String>,
        mut coach_state: Signal<CoachState>,
    ) -> Option<Rc<CoachCheckpointListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |event: web_sys::Event| {
            let event_value = event.as_ref();
            let Ok(detail) = js_sys::Reflect::get(event_value, &JsValue::from_str("detail")) else {
                return;
            };
            let Some(detail_object) = detail.dyn_ref::<js_sys::Object>() else {
                return;
            };
            let keys = js_sys::Object::keys(detail_object);
            let Some(key_names) = keys
                .iter()
                .map(|key| key.as_string())
                .collect::<Option<Vec<_>>>()
            else {
                return;
            };
            if !valid_coach_checkpoint_keys(&key_names) {
                return;
            }
            let read_string = |name: &str| {
                js_sys::Reflect::get(&detail, &JsValue::from_str(name))
                    .ok()?
                    .as_string()
            };
            let Some(assistance_target) = read_string("assistanceTarget") else {
                return;
            };
            let Some(coach_action) = read_string("coachAction") else {
                return;
            };
            let Some(coach_phase) = read_string("coachPhase") else {
                return;
            };
            let Some(respondent_stage) = read_string("respondentStage") else {
                return;
            };
            let Some(checkpoint_route) = read_string("route") else {
                return;
            };
            let Some(checkpoint) = read_string("sessionState") else {
                return;
            };
            let Ok(version) = js_sys::Reflect::get(&detail, &JsValue::from_str("version")) else {
                return;
            };
            let Some(coach_phase) = coach_phase_from_checkpoint(&coach_phase) else {
                return;
            };
            let Some(coach_action) = coach_action_from_checkpoint(&coach_action) else {
                return;
            };
            let Some(version) = version.as_f64() else {
                return;
            };
            if !valid_coach_checkpoint_metadata(
                &checkpoint,
                &checkpoint_route,
                &assistance_target,
                &respondent_stage,
                coach_phase,
                coach_action,
                version,
                keys.length(),
            ) {
                return;
            }

            // Commit the complete finite, server-authoritative tuple. A
            // network failure after this event can rearm without reconstructing
            // state from response prose or hardcoded client assumptions.
            session_state.set(checkpoint);
            route.set(checkpoint_route);
            coach_state.set(CoachState::from_result(coach_phase, coach_action));
        });
        window
            .add_event_listener_with_callback(
                "kotae:coach-checkpoint",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(CoachCheckpointListener { window, callback }))
    }

    pub fn end_turn() {
        let _ = end_turn_js();
    }

    pub fn install_voice_interrupted_listener(
        mut voice_state: Signal<VoiceState>,
    ) -> Option<Rc<VoiceInterruptedListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
            if matches!(
                *voice_state.peek(),
                VoiceState::Thinking | VoiceState::Speaking
            ) {
                voice_state.set(VoiceState::Listening);
            }
        });
        window
            .add_event_listener_with_callback(
                "kotae:voice-interrupted",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(VoiceInterruptedListener { window, callback }))
    }

    pub fn install_voice_session_paused_listener(
        mut voice_state: Signal<VoiceState>,
        mut generation: Signal<u64>,
    ) -> Option<Rc<VoiceSessionPausedListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |event: web_sys::Event| {
            let event_value = event.as_ref();
            let Ok(detail) = js_sys::Reflect::get(event_value, &JsValue::from_str("detail")) else {
                return;
            };
            let Some(detail_object) = detail.dyn_ref::<js_sys::Object>() else {
                return;
            };
            let keys = js_sys::Object::keys(detail_object);
            let Ok(reason) = js_sys::Reflect::get(&detail, &JsValue::from_str("reason")) else {
                return;
            };
            let Ok(version) = js_sys::Reflect::get(&detail, &JsValue::from_str("version")) else {
                return;
            };
            let Some(reason) = reason.as_string() else {
                return;
            };
            let Some(version) = version.as_f64() else {
                return;
            };
            if !valid_voice_pause_metadata(&reason, version, keys.length()) {
                return;
            }

            let current_state = *voice_state.peek();
            if session_stop_pauses(current_state) {
                if stop_session_js().is_err() {
                    return;
                }
                let next = generation.peek().wrapping_add(1);
                generation.set(next);
                // The opaque encrypted conversation state is intentionally
                // left untouched. Resume is a new explicit gesture and may
                // reuse that state without reacquiring a microphone in the
                // background.
                voice_state.set(VoiceState::Paused);
            }
        });
        window
            .add_event_listener_with_callback(
                "kotae:voice-session-paused",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        Some(Rc::new(VoiceSessionPausedListener { window, callback }))
    }

    pub fn stop_session() {
        let _ = stop_session_js();
    }

    fn error_code(error: JsValue) -> Option<String> {
        js_sys::Error::from(error).message().as_string()
    }

    fn user_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
            Some("voice_turn_timeout") => {
                "今の声は届いています　こちらの返事だけ間に合いませんでした　言い直さなくて大丈夫です"
            }
            Some("voice_turn_unavailable") => {
                "今の声は届いています　こちらの処理だけ止まりました　言い直さずそのまま続けられます"
            }
            Some("microphone_unsupported") => {
                "このブラウザでは音声会話を使えない　最新版でためしてみて"
            }
            Some("microphone_permission_denied") => {
                "マイクが許可されていない　ブラウザの権限を確認してみて"
            }
            Some("microphone_unavailable") => "使えるマイクが見つからない　接続を確認してみて",
            Some("no_speech") => {
                "声を待っています　言い直そうとせず　続きや別のひと言をそのままどうぞ"
            }
            Some("authentication_failed") => "安全な接続を確認できない　もう一度ためしてみて",
            Some("account_boundary_changed") => ACCOUNT_BOUNDARY_CHANGED_COPY,
            Some("identity_required") | Some("identity_verification_failed") => {
                "アカウント状態を安全に確認できませんでした　マイクは開いていません"
            }
            Some("passkey_required") => PASSKEY_REQUIRED_COPY,
            Some("passkey_cancelled") => PASSKEY_CANCELLED_COPY,
            Some("passkey_unsupported") => PASSKEY_UNSUPPORTED_COPY,
            Some("passkey_authentication_failed") => PASSKEY_AUTHENTICATION_FAILED_COPY,
            Some("passkey_registration_cancelled") => PASSKEY_REGISTRATION_CANCELLED_COPY,
            Some("passkey_registration_failed") => PASSKEY_REGISTRATION_FAILED_COPY,
            Some("passkey_registration_recovery_required") => {
                PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY
            }
            Some("passkey_account_exists") => PASSKEY_ACCOUNT_EXISTS_COPY,
            Some("app_check_not_configured") => "App Check の公開サイトキーがまだない",
            Some("voice_turn_too_large") => "少し長すぎた　短く区切ってみて",
            Some("voice_turn_invalid") => "音声を確認できない　もう一度ためしてみて",
            Some("strict_privacy_blocked") => STRICT_PRIVACY_BLOCKED_COPY,
            Some("rate_limited") => {
                "いま少し混み合っています　会話は開いたままです　そのまま続けられます"
            }
            Some("request_cancelled") => "会話を一時停止した",
            Some("session_expired") => "安全のためマイクを閉じた　もう一度すぐ始められる",
            Some("audio_playback_blocked") => "声を再生できない　端末の消音設定を確認してみて",
            Some("voice_api_unavailable") => {
                "こちらの返事の準備だけ止まりました　会話は開いたままです　言い直さなくて大丈夫です"
            }
            _ => "音声エージェントにつながらない　もう一度ためしてみて",
        }
    }
}

fn requires_passkey_choice(cloud_state: CloudState, voice_state: VoiceState) -> bool {
    if !matches!(voice_state, VoiceState::Ready | VoiceState::Error(_)) {
        return false;
    }

    matches!(
        cloud_state,
        CloudState::IdentityRequired
            | CloudState::PasskeyRequired
            | CloudState::PasskeyRegistrationRecoveryRequired
    ) || matches!(
        voice_state,
        VoiceState::Error(message)
            if [
                PASSKEY_REQUIRED_COPY,
                PASSKEY_CANCELLED_COPY,
                PASSKEY_UNSUPPORTED_COPY,
                PASSKEY_AUTHENTICATION_FAILED_COPY,
                PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY,
            ]
            .contains(&message)
    )
}

fn requires_passkey_registration_recovery(
    cloud_state: CloudState,
    voice_state: VoiceState,
    feedback: Option<PasskeySetupFeedback>,
) -> bool {
    cloud_state == CloudState::PasskeyRegistrationRecoveryRequired
        || matches!(
            voice_state,
            VoiceState::Error(PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY)
        )
        || matches!(
            feedback,
            Some(PasskeySetupFeedback::Error(
                PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY
            ))
        )
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PasskeyFocusTarget {
    NewAccount,
    ReturningAccount,
    VoiceStart,
}

impl PasskeyFocusTarget {
    const fn element_id(self) -> &'static str {
        match self {
            Self::NewAccount => "new-passkey-account-action",
            Self::ReturningAccount => "returning-passkey-account-action",
            Self::VoiceStart => "voice-start-action",
        }
    }
}

fn passkey_focus_target(
    voice_state: VoiceState,
    feedback: Option<PasskeySetupFeedback>,
    cloud_state: CloudState,
) -> Option<PasskeyFocusTarget> {
    match feedback {
        Some(PasskeySetupFeedback::Error(message)) => {
            if [
                PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY,
                PASSKEY_ACCOUNT_EXISTS_COPY,
            ]
            .contains(&message)
            {
                Some(PasskeyFocusTarget::ReturningAccount)
            } else {
                Some(PasskeyFocusTarget::NewAccount)
            }
        }
        Some(PasskeySetupFeedback::Success(_)) => Some(PasskeyFocusTarget::VoiceStart),
        None if cloud_state == CloudState::PasskeyRegistrationRecoveryRequired => {
            Some(PasskeyFocusTarget::ReturningAccount)
        }
        None if requires_passkey_choice(cloud_state, voice_state)
            && matches!(voice_state, VoiceState::Error(_)) =>
        {
            Some(PasskeyFocusTarget::ReturningAccount)
        }
        None => None,
    }
}

const fn cloud_state_for_display(
    cloud_state: CloudState,
    passkey_gate_visible: bool,
) -> CloudState {
    if passkey_gate_visible && matches!(cloud_state, CloudState::Ready) {
        CloudState::PasskeyRequired
    } else {
        cloud_state
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod cloud {
    use super::{
        CloudState, CoachState, DocumentInfo, FinishTurnError, PasskeySetupFeedback,
        ResearchRecord, ResearchStatus, VoiceReceipt, VoiceStartLatency, VoiceState, VoiceTurnMode,
        VoiceTurnResult, WaitTurnError,
    };
    use dioxus::prelude::Signal;

    #[derive(Clone)]
    pub struct Listener;

    pub async fn status() -> CloudState {
        CloudState::Unavailable
    }

    pub async fn register_passkey_account() -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn begin_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
        _strict_cloud_minimization: bool,
        _coach_active: bool,
    ) -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn wait_for_turn_end() -> Result<bool, WaitTurnError> {
        Err(WaitTurnError::Terminal("WebAssembly版で使ってみて"))
    }

    pub async fn finish_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
        _strict_cloud_minimization: bool,
    ) -> Result<VoiceTurnResult, FinishTurnError> {
        Err(FinishTurnError::Message("WebAssembly版で使ってみて"))
    }

    pub fn install_document_clear_listener(
        _document_info: Signal<Option<DocumentInfo>>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_account_access_refresh_listener(
        _cloud_status_refresh: Signal<u64>,
    ) -> Option<Listener> {
        None
    }

    #[allow(clippy::too_many_arguments)]
    pub fn install_account_boundary_changed_listener(
        _voice_state: Signal<VoiceState>,
        _generation: Signal<u64>,
        _session_state: Signal<String>,
        _detected_domain: Signal<String>,
        _route: Signal<String>,
        _coach_state: Signal<CoachState>,
        _needs_paper: Signal<bool>,
        _research_status: Signal<ResearchStatus>,
        _research_records: Signal<Vec<ResearchRecord>>,
        _document_info: Signal<Option<DocumentInfo>>,
        _document_error: Signal<Option<&'static str>>,
        _caption: Signal<Option<String>>,
        _passkey_setup_feedback: Signal<Option<PasskeySetupFeedback>>,
        _cloud_status_refresh: Signal<u64>,
    ) -> Option<Listener> {
        None
    }

    pub fn focus_element(_element_id: &str) {}

    pub fn install_voice_receipt_listener(
        _voice_receipt: Signal<VoiceReceipt>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_voice_input_confirmed_listener(
        _coach_state: Signal<CoachState>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_voice_start_latency_listener(
        _voice_start_latency: Signal<Option<VoiceStartLatency>>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_first_audio_listener(
        _voice_state: Signal<VoiceState>,
        _voice_receipt: Signal<VoiceReceipt>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_coach_checkpoint_listener(
        _session_state: Signal<String>,
        _route: Signal<String>,
        _coach_state: Signal<CoachState>,
    ) -> Option<Listener> {
        None
    }

    pub fn end_turn() {}

    pub fn install_voice_interrupted_listener(
        _voice_state: Signal<VoiceState>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_voice_session_paused_listener(
        _voice_state: Signal<VoiceState>,
        _generation: Signal<u64>,
    ) -> Option<Listener> {
        None
    }

    pub fn stop_session() {}
}

fn main() {
    dioxus::launch(App);
}

#[allow(clippy::too_many_arguments)]
fn arm_listening(
    operation: u64,
    announce_permission: bool,
    turn_mode: VoiceTurnMode,
    mut voice_state: Signal<VoiceState>,
    mut generation: Signal<u64>,
    mut session_state: Signal<String>,
    mut detected_domain: Signal<String>,
    mut route: Signal<String>,
    mut coach_state: Signal<CoachState>,
    mut needs_paper: Signal<bool>,
    mut research_status: Signal<ResearchStatus>,
    mut research_records: Signal<Vec<ResearchRecord>>,
    mut document_info: Signal<Option<DocumentInfo>>,
    mut caption: Signal<Option<String>>,
    mut turn_notice: Signal<TurnNotice>,
    strict_cloud_minimization: Signal<bool>,
) {
    if announce_permission {
        voice_state.set(VoiceState::RequestingPermission);
    }

    spawn(async move {
        let strict_snapshot = *strict_cloud_minimization.peek();
        // This snapshot belongs to the same session-start boundary as the
        // opaque state below. Completion and release remain visible in the UI
        // but no longer pin a later turn to the staged coach route.
        let coach_active_snapshot = coach_state.peek().requires_staged_route();
        let state_snapshot = if strict_snapshot {
            String::new()
        } else {
            session_state.peek().clone()
        };
        if let Err(message) = cloud::begin_turn(
            &state_snapshot,
            turn_mode,
            strict_snapshot,
            coach_active_snapshot,
        )
        .await
        {
            if *generation.peek() == operation {
                if message == ACCOUNT_BOUNDARY_CHANGED_COPY {
                    let next = generation.peek().wrapping_add(1);
                    generation.set(next);
                    cloud::stop_session();
                    session_state.set(String::new());
                    detected_domain.set(String::new());
                    route.set(String::new());
                    coach_state.set(CoachState::NONE);
                    needs_paper.set(false);
                    research_status.set(ResearchStatus::None);
                    research_records.set(Vec::new());
                    document_info.set(None);
                    caption.set(None);
                    voice_state.set(VoiceState::Error(ACCOUNT_BOUNDARY_CHANGED_COPY));
                    return;
                }
                cloud::stop_session();
                document_info.set(None);
                voice_state.set(VoiceState::Error(message));
            }
            return;
        }
        if *generation.peek() != operation {
            cloud::stop_session();
            return;
        }

        voice_state.set(VoiceState::Listening);
        let has_speech = match cloud::wait_for_turn_end().await {
            Ok(has_speech) => has_speech,
            Err(WaitTurnError::Recoverable(_message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    // The failed encoded capture has already been discarded.
                    // Keep the opaque session and wait for a fresh foreground
                    // utterance; never resend bytes from the oversized turn.
                    turn_notice.set(TurnNotice::CaptureSkipped);
                    arm_listening(
                        operation,
                        false,
                        VoiceTurnMode::Foreground,
                        voice_state,
                        generation,
                        session_state,
                        detected_domain,
                        route,
                        coach_state,
                        needs_paper,
                        research_status,
                        research_records,
                        document_info,
                        caption,
                        turn_notice,
                        strict_cloud_minimization,
                    );
                }
                return;
            }
            Err(WaitTurnError::Terminal(message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    cloud::stop_session();
                    document_info.set(None);
                    voice_state.set(VoiceState::Error(message));
                }
                return;
            }
        };

        if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
            if has_speech {
                submit_turn(
                    operation,
                    turn_mode,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    coach_state,
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
                    caption,
                    turn_notice,
                    strict_cloud_minimization,
                );
            } else {
                // A silent bounded window carries no speech and consumes no
                // conversational turn. Keep the explicitly opened session
                // alive in foreground mode; the independent three-minute
                // idle and thirty-minute absolute clocks remain terminal.
                // Automatic windows never inherit intentional authority.
                arm_listening(
                    operation,
                    false,
                    VoiceTurnMode::Foreground,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    coach_state,
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
                    caption,
                    turn_notice,
                    strict_cloud_minimization,
                );
            }
        }
    });
}

#[allow(clippy::too_many_arguments)]
fn resume_foreground_interruption(
    operation: u64,
    mut voice_state: Signal<VoiceState>,
    generation: Signal<u64>,
    session_state: Signal<String>,
    detected_domain: Signal<String>,
    route: Signal<String>,
    coach_state: Signal<CoachState>,
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
    mut turn_notice: Signal<TurnNotice>,
    strict_cloud_minimization: Signal<bool>,
) {
    voice_state.set(VoiceState::Listening);
    spawn(async move {
        let has_speech = match cloud::wait_for_turn_end().await {
            Ok(has_speech) => has_speech,
            Err(WaitTurnError::Recoverable(_message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    turn_notice.set(TurnNotice::CaptureSkipped);
                    arm_listening(
                        operation,
                        false,
                        VoiceTurnMode::Foreground,
                        voice_state,
                        generation,
                        session_state,
                        detected_domain,
                        route,
                        coach_state,
                        needs_paper,
                        research_status,
                        research_records,
                        document_info,
                        caption,
                        turn_notice,
                        strict_cloud_minimization,
                    );
                }
                return;
            }
            Err(WaitTurnError::Terminal(message)) => {
                if *generation.peek() == operation {
                    cloud::stop_session();
                    voice_state.set(VoiceState::Error(message));
                }
                return;
            }
        };
        if *generation.peek() != operation || *voice_state.peek() != VoiceState::Listening {
            return;
        }
        if has_speech {
            // Acoustic interruption expects a conversational response but is
            // never granted fresh-gesture authority.
            submit_turn(
                operation,
                VoiceTurnMode::Foreground,
                voice_state,
                generation,
                session_state,
                detected_domain,
                route,
                coach_state,
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
                turn_notice,
                strict_cloud_minimization,
            );
        } else {
            arm_listening(
                operation,
                false,
                VoiceTurnMode::Foreground,
                voice_state,
                generation,
                session_state,
                detected_domain,
                route,
                coach_state,
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
                turn_notice,
                strict_cloud_minimization,
            );
        }
    });
}

#[allow(clippy::too_many_arguments)]
fn submit_turn(
    operation: u64,
    turn_mode: VoiceTurnMode,
    mut voice_state: Signal<VoiceState>,
    generation: Signal<u64>,
    mut session_state: Signal<String>,
    mut detected_domain: Signal<String>,
    mut route: Signal<String>,
    mut coach_state: Signal<CoachState>,
    mut needs_paper: Signal<bool>,
    mut research_status: Signal<ResearchStatus>,
    mut research_records: Signal<Vec<ResearchRecord>>,
    mut document_info: Signal<Option<DocumentInfo>>,
    mut caption: Signal<Option<String>>,
    mut turn_notice: Signal<TurnNotice>,
    strict_cloud_minimization: Signal<bool>,
) {
    if *generation.peek() != operation || *voice_state.peek() != VoiceState::Listening {
        return;
    }

    let strict_snapshot = *strict_cloud_minimization.peek();
    let state_snapshot = if strict_snapshot {
        String::new()
    } else {
        session_state.peek().clone()
    };
    let consumed_document = !strict_snapshot && document_info.peek().is_some();
    research_status.set(ResearchStatus::None);
    research_records.set(Vec::new());
    turn_notice.set(TurnNotice::Clear);
    let coach_without_proof = (*coach_state.peek()).without_answer_proof();
    coach_state.set(coach_without_proof);
    voice_state.set(VoiceState::Thinking);

    spawn(async move {
        let result = cloud::finish_turn(&state_snapshot, turn_mode, strict_snapshot).await;
        if *generation.peek() != operation {
            return;
        }

        let result = match result {
            Ok(result) => result,
            Err(FinishTurnError::Interrupted) => {
                if consumed_document {
                    document_info.set(None);
                }
                resume_foreground_interruption(
                    operation,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    coach_state,
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
                    caption,
                    turn_notice,
                    strict_cloud_minimization,
                );
                return;
            }
            Err(FinishTurnError::Recoverable(_message)) => {
                if consumed_document {
                    document_info.set(None);
                }
                // The captured turn was consumed and must never be resent. A
                // transient provider or network failure also must not revoke
                // the user's foreground microphone gesture: keep the opaque
                // pre-turn state, leave the session open, and listen for a new
                // utterance. Publish a content-free notice independently from
                // optional model captions so it remains visible with CC off.
                turn_notice.set(TurnNotice::ReplyUnavailable);
                arm_listening(
                    operation,
                    false,
                    VoiceTurnMode::Foreground,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    coach_state,
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
                    caption,
                    turn_notice,
                    strict_cloud_minimization,
                );
                return;
            }
            Err(FinishTurnError::Message(message)) => {
                if consumed_document {
                    document_info.set(None);
                }
                cloud::stop_session();
                voice_state.set(VoiceState::Error(message));
                return;
            }
        };

        if !valid_answer_proof_metadata(
            result.answer_proof,
            &result.assistance_target,
            &result.respondent_stage,
            result.coach_phase,
            result.coach_action,
            strict_snapshot,
            consumed_document,
        ) {
            cloud::stop_session();
            voice_state.set(VoiceState::Error(
                "回答確認の境界を検証できないため停止しました",
            ));
            return;
        }

        if !valid_voice_privacy_metadata(
            strict_snapshot,
            &result.privacy_status,
            &result.session_state,
            result.research_status,
            result.research_records.len(),
        ) {
            cloud::stop_session();
            voice_state.set(VoiceState::Error(
                "プライバシー境界を確認できないため停止しました",
            ));
            return;
        }

        if result.privacy_status == "blocked" {
            session_state.set(String::new());
            detected_domain.set(String::new());
            route.set(result.route.clone());
            coach_state.set(CoachState::NONE);
            needs_paper.set(false);
            research_status.set(ResearchStatus::None);
            research_records.set(Vec::new());
            document_info.set(None);
            caption.set(Some(STRICT_PRIVACY_BLOCKED_COPY.to_string()));
            turn_notice.set(TurnNotice::PrivacyBlocked);
            arm_listening(
                operation,
                false,
                VoiceTurnMode::Foreground,
                voice_state,
                generation,
                session_state,
                detected_domain,
                route,
                coach_state,
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
                turn_notice,
                strict_cloud_minimization,
            );
            return;
        }

        if !valid_streamed_audio_metadata(
            &result.audio_base64,
            &result.audio_mime_type,
            result.streamed_audio,
        ) {
            cloud::stop_session();
            voice_state.set(VoiceState::Error(
                "音声応答を確認できない　もう一度ためしてみて",
            ));
            return;
        }
        let spoke = result.streamed_audio;
        turn_notice.set(TurnNotice::Clear);
        session_state.set(result.session_state.clone());
        detected_domain.set(result.detected_domain.clone());
        route.set(result.route.clone());
        needs_paper.set(result.needs_paper);
        research_status.set(result.research_status);
        research_records.set(result.research_records.clone());
        caption.set(result.caption.clone());
        if consumed_document {
            document_info.set(None);
        }
        if turn_mode == VoiceTurnMode::Foreground && silent_recognition_miss(&result.route) {
            // A provider-authenticated STT miss is a no-op turn, not a reason
            // to end the conversation. Continue in foreground mode without
            // granting intentional authority; the independent idle,
            // absolute-session, and rate limits keep retries bounded.
            arm_listening(
                operation,
                false,
                VoiceTurnMode::Foreground,
                voice_state,
                generation,
                session_state,
                detected_domain,
                route,
                coach_state,
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
                turn_notice,
                strict_cloud_minimization,
            );
            return;
        }
        let answer_proof = if result.interrupted {
            AnswerProof::None
        } else {
            result.answer_proof
        };
        coach_state.set(CoachState::from_authoritative_result(
            result.coach_phase,
            result.coach_action,
            answer_proof,
        ));
        if result.interrupted {
            // The final frame reached a clean terminal EOF, so commit its
            // state before submitting the already-captured interruption.
            resume_foreground_interruption(
                operation,
                voice_state,
                generation,
                session_state,
                detected_domain,
                route,
                coach_state,
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
                turn_notice,
                strict_cloud_minimization,
            );
            return;
        }

        if spoke && *voice_state.peek() == VoiceState::Thinking {
            voice_state.set(VoiceState::Speaking);
        }
        if *generation.peek() != operation {
            return;
        }

        arm_listening(
            operation,
            false,
            VoiceTurnMode::Foreground,
            voice_state,
            generation,
            session_state,
            detected_domain,
            route,
            coach_state,
            needs_paper,
            research_status,
            research_records,
            document_info,
            caption,
            turn_notice,
            strict_cloud_minimization,
        );
    });
}

#[allow(clippy::too_many_arguments)]
fn start_or_resume(
    voice_state: Signal<VoiceState>,
    mut generation: Signal<u64>,
    session_state: Signal<String>,
    detected_domain: Signal<String>,
    route: Signal<String>,
    coach_state: Signal<CoachState>,
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
    mut turn_notice: Signal<TurnNotice>,
    strict_cloud_minimization: Signal<bool>,
) {
    turn_notice.set(TurnNotice::Clear);
    let operation = generation.peek().wrapping_add(1);
    generation.set(operation);
    arm_listening(
        operation,
        true,
        turn_mode_for_gesture_epoch(true),
        voice_state,
        generation,
        session_state,
        detected_domain,
        route,
        coach_state,
        needs_paper,
        research_status,
        research_records,
        document_info,
        caption,
        turn_notice,
        strict_cloud_minimization,
    );
}

const fn turn_mode_for_gesture_epoch(fresh_gesture: bool) -> VoiceTurnMode {
    if fresh_gesture {
        VoiceTurnMode::Intentional
    } else {
        VoiceTurnMode::Foreground
    }
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
const fn session_stop_pauses(state: VoiceState) -> bool {
    !matches!(state, VoiceState::Ready | VoiceState::Paused)
}

fn valid_voice_receipt_metadata(phase: &str, version: f64, field_count: u32) -> bool {
    field_count == 2 && version == 1.0 && matches!(phase, "received" | "clear")
}

fn validated_voice_start_latency(
    milliseconds: Option<f64>,
    version: f64,
    keys: &[String],
) -> Option<Option<VoiceStartLatency>> {
    if version != 1.0 || keys != ["milliseconds", "version"] {
        return None;
    }
    let Some(milliseconds) = milliseconds else {
        return Some(None);
    };
    if !milliseconds.is_finite()
        || !(0.0..=VoiceStartLatency::MAXIMUM_EVENT_MS).contains(&milliseconds)
    {
        return None;
    }
    Some(Some(VoiceStartLatency {
        milliseconds: milliseconds.round() as u32,
    }))
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn confirmed_voice_input_state(
    state: CoachState,
    version: f64,
    keys: &[String],
) -> Option<CoachState> {
    if version == 1.0 && keys == ["version"] {
        Some(state.without_answer_proof())
    } else {
        None
    }
}

fn valid_answer_proof_metadata(
    proof: AnswerProof,
    assistance_target: &str,
    respondent_stage: &str,
    coach_phase: CoachPhase,
    coach_action: CoachAction,
    strict_cloud_minimization: bool,
    consumed_document: bool,
) -> bool {
    match proof {
        AnswerProof::None => true,
        AnswerProof::QuestionBoundInputAnswerFirst => {
            !strict_cloud_minimization
                && !consumed_document
                && assistance_target == "respondent"
                && respondent_stage == "restructure"
                && matches!(
                    (coach_phase, coach_action),
                    (CoachPhase::Complete, CoachAction::Complete)
                        | (CoachPhase::Expanding, CoachAction::Expand)
                )
        }
    }
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn coach_phase_from_checkpoint(value: &str) -> Option<CoachPhase> {
    match value {
        "awaiting_answer" => Some(CoachPhase::AwaitingAnswer),
        "awaiting_restatement" => Some(CoachPhase::AwaitingRestatement),
        "expanding" => Some(CoachPhase::Expanding),
        "complete" => Some(CoachPhase::Complete),
        "blocked" => Some(CoachPhase::Blocked),
        _ => None,
    }
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn coach_action_from_checkpoint(value: &str) -> Option<CoachAction> {
    match value {
        "elicit" => Some(CoachAction::Elicit),
        "restate" => Some(CoachAction::Restate),
        "expand" => Some(CoachAction::Expand),
        "complete" => Some(CoachAction::Complete),
        "retry" => Some(CoachAction::Retry),
        "release" => Some(CoachAction::Release),
        _ => None,
    }
}

#[allow(clippy::too_many_arguments)]
fn valid_coach_checkpoint_metadata(
    session_state: &str,
    route: &str,
    assistance_target: &str,
    respondent_stage: &str,
    coach_phase: CoachPhase,
    coach_action: CoachAction,
    version: f64,
    field_count: u32,
) -> bool {
    field_count == 7
        && version == 1.0
        && !session_state.is_empty()
        && session_state.encode_utf16().count() <= COACH_CHECKPOINT_MAX_CHARS
        && session_state.trim() == session_state
        && !session_state.chars().any(char::is_control)
        && route == NATIVE_RESPONDENT_COACH_ROUTE
        && assistance_target == "respondent"
        && matches!(respondent_stage, "awaiting_answer" | "restructure")
        && matches!(
            (coach_phase, coach_action),
            (CoachPhase::AwaitingAnswer, CoachAction::Elicit)
                | (CoachPhase::AwaitingRestatement, CoachAction::Restate)
                | (CoachPhase::Expanding, CoachAction::Expand)
                | (CoachPhase::Complete, CoachAction::Complete)
                | (CoachPhase::Blocked, CoachAction::Retry)
                | (CoachPhase::Blocked, CoachAction::Release)
        )
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn valid_coach_checkpoint_keys(keys: &[String]) -> bool {
    const EXPECTED: [&str; 7] = [
        "assistanceTarget",
        "coachAction",
        "coachPhase",
        "respondentStage",
        "route",
        "sessionState",
        "version",
    ];
    keys.len() == EXPECTED.len()
        && EXPECTED
            .iter()
            .all(|expected| keys.iter().any(|key| key == expected))
}

const fn valid_voice_pause_metadata(reason: &str, version: f64, field_count: u32) -> bool {
    field_count == 2
        && version == 1.0
        && matches!(
            reason.as_bytes(),
            b"idle" | b"maximum" | b"hidden" | b"pagehide" | b"microphone_lost"
        )
}

fn silent_recognition_miss(route: &str) -> bool {
    matches!(route, "stt-silent-no-speech" | "stt-silent-low-confidence")
}

fn valid_streamed_audio_metadata(
    audio_base64: &str,
    audio_mime_type: &str,
    streamed_audio: bool,
) -> bool {
    audio_base64.is_empty() && audio_mime_type == if streamed_audio { "audio/L16" } else { "" }
}

const fn valid_voice_privacy_metadata(
    expected_strict: bool,
    privacy_status: &str,
    session_state: &str,
    research_status: ResearchStatus,
    research_record_count: usize,
) -> bool {
    if expected_strict {
        matches!(privacy_status.as_bytes(), b"clear" | b"blocked")
            && session_state.is_empty()
            && matches!(research_status, ResearchStatus::None)
            && research_record_count == 0
    } else {
        privacy_status.is_empty()
    }
}

#[component]
fn LongitudinalPanel() -> Element {
    let mut evaluation_state = use_signal(longitudinal::EvaluationState::default);
    let mut evaluation_step = use_signal(|| EvaluationStep::Prompt);
    let mut evaluation_outcome = use_signal(|| None::<longitudinal::EvaluationOutcome>);
    let mut enjoyment = use_signal(|| None::<longitudinal::OptionalRating>);
    let mut agency = use_signal(|| None::<longitudinal::OptionalRating>);
    let mut burden = use_signal(|| None::<longitudinal::OptionalRating>);
    let mut evaluation_notice = use_signal(|| None::<&'static str>);
    let mut delete_armed = use_signal(|| false);

    let view = evaluation_state.read().view();
    let observations = evaluation_state.read().observations();
    let schedule = longitudinal::schedule(&evaluation_state.read());
    let step = *evaluation_step.read();
    let selected_outcome = *evaluation_outcome.read();
    let selected_enjoyment = *enjoyment.read();
    let selected_agency = *agency.read();
    let selected_burden = *burden.read();
    let delete_is_armed = *delete_armed.read();
    let can_save = selected_outcome.is_some()
        && selected_enjoyment.is_some()
        && selected_agency.is_some()
        && selected_burden.is_some();

    let body = match view {
        longitudinal::EvaluationView::Dormant => rsx! {
            p {
                "開始を押すまで保存領域を読みません。通常会話とは別の任意測定で、集団データの送信はありません。"
            }
            nav { class: "session-controls", aria_label: "任意測定を開始",
                button {
                    class: "control-button",
                    r#type: "button",
                    onclick: move |_| {
                        let next = longitudinal::opt_in_and_start();
                        let notice = match next.view() {
                            longitudinal::EvaluationView::Active { .. } => {
                                "端末内測定を開始しました。"
                            }
                            longitudinal::EvaluationView::Invalid => {
                                "端末内記録を検証できないため、開始しませんでした。"
                            }
                            _ => "端末内保存を安全に使えないため、開始しませんでした。",
                        };
                        evaluation_state.set(next);
                        evaluation_step.set(EvaluationStep::Prompt);
                        evaluation_outcome.set(None);
                        enjoyment.set(None);
                        agency.set(None);
                        burden.set(None);
                        evaluation_notice.set(Some(notice));
                    },
                    "説明に同意して任意測定を開始"
                }
            }
        },
        longitudinal::EvaluationView::Active { event_count } => {
            let schedule_body = match schedule {
                longitudinal::ScheduleView::Due {
                    timepoint,
                    question,
                    days_remaining,
                    completed,
                } => {
                    if step == EvaluationStep::Prompt {
                        rsx! {
                            p { role: "status",
                                strong { "{timepoint}の固定質問" }
                                " / 記録済み {completed} 回 / あと {days_remaining} 日"
                            }
                            p { "{question}" }
                            p {
                                "この測定ではマイクを開きません。質問を見て声に出して答え、答え文を入力せず次へ進んでください。"
                            }
                            nav { class: "session-controls", aria_label: "固定質問への回答",
                                button {
                                    class: "control-button",
                                    r#type: "button",
                                    onclick: move |_| {
                                        evaluation_step.set(EvaluationStep::SelfReport);
                                        evaluation_notice.set(None);
                                    },
                                    "声で答え終わった"
                                }
                            }
                        }
                    } else {
                        rsx! {
                            p { strong { "答え方を自分で分類" } }
                            div { class: "session-controls", role: "group", aria_label: "答え方",
                                for outcome in longitudinal::EvaluationOutcome::ALL {
                                    button {
                                        class: if selected_outcome == Some(outcome) {
                                            "control-button is-active"
                                        } else {
                                            "control-button"
                                        },
                                        r#type: "button",
                                        aria_pressed: selected_outcome == Some(outcome),
                                        onclick: move |_| evaluation_outcome.set(Some(outcome)),
                                        {outcome.label()}
                                    }
                                }
                            }
                            p { strong { "楽しさ　1（低い）〜5（高い）" } }
                            div { class: "session-controls", role: "group", aria_label: "楽しさ",
                                for rating in longitudinal::OptionalRating::ALL {
                                    button {
                                        class: if selected_enjoyment == Some(rating) {
                                            "control-button is-active"
                                        } else {
                                            "control-button"
                                        },
                                        r#type: "button",
                                        aria_pressed: selected_enjoyment == Some(rating),
                                        onclick: move |_| enjoyment.set(Some(rating)),
                                        {rating.label()}
                                    }
                                }
                            }
                            p { strong { "自分で答えた感覚　1（低い）〜5（高い）" } }
                            div { class: "session-controls", role: "group", aria_label: "自分で答えた感覚",
                                for rating in longitudinal::OptionalRating::ALL {
                                    button {
                                        class: if selected_agency == Some(rating) {
                                            "control-button is-active"
                                        } else {
                                            "control-button"
                                        },
                                        r#type: "button",
                                        aria_pressed: selected_agency == Some(rating),
                                        onclick: move |_| agency.set(Some(rating)),
                                        {rating.label()}
                                    }
                                }
                            }
                            p { strong { "負担　1（低い）〜5（高い）" } }
                            div { class: "session-controls", role: "group", aria_label: "負担",
                                for rating in longitudinal::OptionalRating::ALL {
                                    button {
                                        class: if selected_burden == Some(rating) {
                                            "control-button is-active"
                                        } else {
                                            "control-button"
                                        },
                                        r#type: "button",
                                        aria_pressed: selected_burden == Some(rating),
                                        onclick: move |_| burden.set(Some(rating)),
                                        {rating.label()}
                                    }
                                }
                            }
                            nav { class: "session-controls", aria_label: "自己記録を保存",
                                button {
                                    class: "control-button",
                                    r#type: "button",
                                    disabled: !can_save,
                                    onclick: move |_| {
                                        let (
                                            Some(outcome),
                                            Some(enjoyment_rating),
                                            Some(agency_rating),
                                            Some(burden_rating),
                                        ) = (
                                            *evaluation_outcome.read(),
                                            *enjoyment.read(),
                                            *agency.read(),
                                            *burden.read(),
                                        ) else {
                                            evaluation_notice.set(Some("4項目を選んでから保存してください。"));
                                            return;
                                        };
                                        let current = evaluation_state.read().clone();
                                        match longitudinal::record_due(
                                            &current,
                                            outcome,
                                            enjoyment_rating,
                                            agency_rating,
                                            burden_rating,
                                        ) {
                                            Ok(next) => {
                                                evaluation_state.set(next);
                                                evaluation_step.set(EvaluationStep::Prompt);
                                                evaluation_outcome.set(None);
                                                enjoyment.set(None);
                                                agency.set(None);
                                                burden.set(None);
                                                evaluation_notice.set(Some(
                                                    "有限分類・1〜5・日単位の測定情報を端末内に保存しました。",
                                                ));
                                            }
                                            Err(error) => evaluation_notice.set(Some(error.message())),
                                        }
                                    },
                                    "端末内に記録"
                                }
                                button {
                                    class: "control-button",
                                    r#type: "button",
                                    onclick: move |_| {
                                        evaluation_step.set(EvaluationStep::Prompt);
                                        evaluation_outcome.set(None);
                                        enjoyment.set(None);
                                        agency.set(None);
                                        burden.set(None);
                                        evaluation_notice.set(None);
                                    },
                                    "質問へ戻る"
                                }
                            }
                        }
                    }
                }
                longitudinal::ScheduleView::Waiting {
                    next_timepoint,
                    days_until,
                    completed,
                    missed,
                } => rsx! {
                    p { role: "status",
                        "記録済み {completed} 回 / 期限を過ぎた回 {missed} 回"
                    }
                    p {
                        "次は{next_timepoint}。あと {days_until} 日で、未使用の固定質問を表示します。期限外の記録は受け付けません。"
                    }
                },
                longitudinal::ScheduleView::Complete { completed, missed } => rsx! {
                    p { role: "status", "測定期間は終了しました。記録済み {completed} 回 / 未記録 {missed} 回" }
                    p { "これは個人内の記録で、改善や因果効果を証明する結果ではありません。" }
                },
                longitudinal::ScheduleView::ClockUnavailable => rsx! {
                    p { role: "alert", "端末の日付を安全に確認できないため、測定は記録しません。" }
                },
                longitudinal::ScheduleView::NotActive => rsx! {},
            };

            rsx! {
                {schedule_body}
                nav { class: "session-controls", aria_label: "測定の同意を管理",
                    button {
                        class: "control-button control-button--end",
                        r#type: "button",
                        onclick: move |_| {
                            let next = longitudinal::withdraw();
                            let notice = if next.view()
                                == longitudinal::EvaluationView::Withdrawn
                            {
                                "測定を停止しました。記録は送信されず、再開または全削除を選べます。"
                            } else {
                                "停止状態を端末内に保存できませんでした。追加記録は行わず、もう一度確認してください。"
                            };
                            evaluation_state.set(next);
                            evaluation_step.set(EvaluationStep::Prompt);
                            evaluation_notice.set(Some(notice));
                        },
                        "測定を撤回・停止"
                    }
                }
                p { "端末内の記録件数: {event_count}" }
                if observations.len() >= 2 {
                    section { aria_label: "時点ごとの測定記録",
                        h3 { "時点ごとの記録" }
                        p {
                            "保存された有限分類をそのまま表示します。時点間の差の自動判定は行いません。"
                        }
                        table {
                            thead {
                                tr {
                                    th { scope: "col", "時点" }
                                    th { scope: "col", "答え方" }
                                    th { scope: "col", "楽しさ" }
                                    th { scope: "col", "自分で答えた感覚" }
                                    th { scope: "col", "負担" }
                                }
                            }
                            tbody {
                                for observation in observations {
                                    tr {
                                        th { scope: "row", {observation.timepoint} }
                                        td { {observation.outcome} }
                                        td { {observation.enjoyment} }
                                        td { {observation.agency} }
                                        td { {observation.burden} }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        longitudinal::EvaluationView::Withdrawn => rsx! {
            p { "測定は撤回・停止中です。停止中は新しい記録を作りません。" }
            nav { class: "session-controls", aria_label: "任意測定を再開",
                button {
                    class: "control-button",
                    r#type: "button",
                    onclick: move |_| {
                        let next = longitudinal::opt_in_and_start();
                        let notice = if matches!(
                            next.view(),
                            longitudinal::EvaluationView::Active { .. }
                        ) {
                            "新しい同意で端末内測定を再開しました。"
                        } else {
                            "端末内保存を安全に確認できないため、再開しませんでした。"
                        };
                        evaluation_state.set(next);
                        evaluation_step.set(EvaluationStep::Prompt);
                        evaluation_notice.set(Some(notice));
                    },
                    "説明に再同意して任意測定を再開"
                }
            }
        },
        longitudinal::EvaluationView::Deleted => rsx! {
            p { "端末内の測定台帳を削除しました。削除した回答記録は復元できません。" }
            nav { class: "session-controls", aria_label: "新しい任意測定",
                button {
                    class: "control-button",
                    r#type: "button",
                    onclick: move |_| {
                        let next = longitudinal::opt_in_and_start();
                        let notice = if matches!(
                            next.view(),
                            longitudinal::EvaluationView::Active { .. }
                        ) {
                            "新しい端末内測定を開始しました。"
                        } else {
                            "端末内保存を安全に確認できないため、開始しませんでした。"
                        };
                        evaluation_state.set(next);
                        evaluation_step.set(EvaluationStep::Prompt);
                        evaluation_notice.set(Some(notice));
                    },
                    "説明に同意して新しく開始"
                }
            }
        },
        longitudinal::EvaluationView::Invalid => rsx! {
            p { role: "alert",
                "端末内記録の形式または同意状態を検証できません。安全のため読まず、追加記録もしません。"
            }
        },
        longitudinal::EvaluationView::StorageUnavailable => rsx! {
            p { role: "alert",
                "端末の保存領域を安全に使えないため、測定は開始・記録されていません。"
            }
            nav { class: "session-controls", aria_label: "端末内測定を再試行",
                button {
                    class: "control-button",
                    r#type: "button",
                    onclick: move |_| {
                        let next = longitudinal::opt_in_and_start();
                        let notice = if matches!(
                            next.view(),
                            longitudinal::EvaluationView::Active { .. }
                        ) {
                            "端末内保存を確認し、測定を開始または再開しました。"
                        } else {
                            "端末内保存を安全に使えないため、測定しません。"
                        };
                        evaluation_state.set(next);
                        evaluation_notice.set(Some(notice));
                    },
                    "もう一度確認"
                }
            }
        },
    };

    rsx! {
        details {
            class: "privacy-fold",
            aria_label: "個人内の推移を測る任意機能",
            summary {
                span { class: "utility-index", "02" }
                span {
                    strong { "任意の自己記録（会話とは別）" }
                    small { "開始を押すまで保存領域を読みません" }
                }
                i { aria_hidden: "true" }
            }
            div { class: "privacy-fold__body",
                p {
                    "長期効果は未実証です。開始時・4週目・8週目・終了4週後・終了12週後の固定質問を、各期限内に一度だけ自己記録します。"
                }
                p {
                    "録音・文字起こし・自由記述・Firebase UID・時刻・応答時間は保存しません。有限分類、1〜5、日単位の測定日、無作為な端末内ID、同意・schema versionに168日の期限を付け、次回アクセス時に期限切れを削除します。外部へは送信しません。"
                }
                p {
                    "全削除後は別タブから回答が復活しないよう、個人IDや回答を含まない固定削除マーカーだけを端末へ残します。この自己記録は暗号署名された研究台帳ではありません。"
                }
                {body}
                if let Some(message) = *evaluation_notice.read() {
                    p { role: "status", aria_live: "polite", {message} }
                }
                if view != longitudinal::EvaluationView::Deleted {
                    nav { class: "session-controls", aria_label: "端末内測定記録を全削除",
                        if delete_is_armed {
                            button {
                                class: "control-button control-button--end",
                                r#type: "button",
                                onclick: move |_| {
                                    let next = longitudinal::delete();
                                    let notice = if next.view()
                                        == longitudinal::EvaluationView::Deleted
                                    {
                                        "端末内の測定台帳を全削除しました。"
                                    } else {
                                        "端末内の測定台帳を全削除できませんでした。ブラウザの保存設定を確認してください。"
                                    };
                                    evaluation_state.set(next);
                                    evaluation_step.set(EvaluationStep::Prompt);
                                    evaluation_outcome.set(None);
                                    enjoyment.set(None);
                                    agency.set(None);
                                    burden.set(None);
                                    delete_armed.set(false);
                                    evaluation_notice.set(Some(notice));
                                },
                                "本当に端末内記録を全削除"
                            }
                            button {
                                class: "control-button",
                                r#type: "button",
                                onclick: move |_| delete_armed.set(false),
                                "削除をやめる"
                            }
                        } else {
                            button {
                                class: "control-button control-button--end",
                                r#type: "button",
                                onclick: move |_| {
                                    delete_armed.set(true);
                                    evaluation_notice.set(Some(
                                        "全削除すると、この端末の測定回答は復元できません。",
                                    ));
                                },
                                "端末内記録を全削除"
                            }
                        }
                    }
                }
            }
        }
    }
}

#[component]
fn App() -> Element {
    let mut voice_state = use_signal(|| VoiceState::Ready);
    let voice_receipt = use_signal(|| VoiceReceipt::Clear);
    let voice_start_latency = use_signal(|| None::<VoiceStartLatency>);
    let mut generation = use_signal(|| 0_u64);
    let mut session_state = use_signal(String::new);
    let mut detected_domain = use_signal(String::new);
    let mut route = use_signal(String::new);
    let mut coach_state = use_signal(|| CoachState::NONE);
    let mut needs_paper = use_signal(|| false);
    let mut research_status = use_signal(|| ResearchStatus::None);
    let mut research_records = use_signal(Vec::<ResearchRecord>::new);
    let mut document_info = use_signal(|| None::<DocumentInfo>);
    let mut document_error = use_signal(|| None::<&'static str>);
    let mut caption = use_signal(|| None::<String>);
    let mut turn_notice = use_signal(|| TurnNotice::Clear);
    let mut captions_visible = use_signal(|| false);
    let mut strict_cloud_minimization = use_signal(|| false);
    let cloud_status_refresh = use_signal(|| 0_u64);
    let mut passkey_setup_busy = use_signal(|| false);
    let mut passkey_setup_feedback = use_signal(|| None::<PasskeySetupFeedback>);
    let _document_clear_listener =
        use_hook(|| cloud::install_document_clear_listener(document_info));
    let _account_access_refresh_listener =
        use_hook(|| cloud::install_account_access_refresh_listener(cloud_status_refresh));
    let _account_boundary_changed_listener = use_hook(|| {
        cloud::install_account_boundary_changed_listener(
            voice_state,
            generation,
            session_state,
            detected_domain,
            route,
            coach_state,
            needs_paper,
            research_status,
            research_records,
            document_info,
            document_error,
            caption,
            passkey_setup_feedback,
            cloud_status_refresh,
        )
    });
    let _voice_receipt_listener = use_hook(|| cloud::install_voice_receipt_listener(voice_receipt));
    let _voice_input_confirmed_listener =
        use_hook(|| cloud::install_voice_input_confirmed_listener(coach_state));
    let _voice_start_latency_listener =
        use_hook(|| cloud::install_voice_start_latency_listener(voice_start_latency));
    let _first_audio_listener =
        use_hook(|| cloud::install_first_audio_listener(voice_state, voice_receipt));
    let _coach_checkpoint_listener =
        use_hook(|| cloud::install_coach_checkpoint_listener(session_state, route, coach_state));
    let _voice_interrupted_listener =
        use_hook(|| cloud::install_voice_interrupted_listener(voice_state));
    let _voice_session_paused_listener =
        use_hook(|| cloud::install_voice_session_paused_listener(voice_state, generation));
    let mut cloud_status = use_resource(move || {
        let _refresh = *cloud_status_refresh.read();
        async move { cloud::status().await }
    });

    use_effect(move || {
        let current_voice_state = *voice_state.read();
        let current_feedback = *passkey_setup_feedback.read();
        let current_cloud_state = cloud_status
            .read()
            .as_ref()
            .copied()
            .unwrap_or(CloudState::Connecting);
        if let Some(target) =
            passkey_focus_target(current_voice_state, current_feedback, current_cloud_state)
        {
            cloud::focus_element(target.element_id());
        }
    });

    let state_snapshot = *voice_state.read();
    let receipt_snapshot = *voice_receipt.read();
    let voice_start_latency_snapshot = *voice_start_latency.read();
    let receipt_is_visible = receipt_snapshot.is_visible_for(state_snapshot);
    let coach_snapshot = *coach_state.read();
    let turn_notice_snapshot = *turn_notice.read();
    let captions_are_visible = *captions_visible.read();
    let strict_mode = *strict_cloud_minimization.read();
    let document_snapshot = document_info.read().clone();
    let research_status_snapshot = *research_status.read();
    let research_snapshot = research_records.read().clone();
    let passkey_setup_is_busy = *passkey_setup_busy.read();
    let passkey_setup_feedback_snapshot = *passkey_setup_feedback.read();
    let prepared_cloud_state = cloud_status
        .read()
        .as_ref()
        .copied()
        .unwrap_or(CloudState::Connecting);
    let effective_cloud_state = prepared_cloud_state;
    let passkey_gate_visible = requires_passkey_choice(effective_cloud_state, state_snapshot);
    let passkey_registration_recovery_required = requires_passkey_registration_recovery(
        effective_cloud_state,
        state_snapshot,
        passkey_setup_feedback_snapshot,
    );
    let displayed_cloud_state =
        cloud_state_for_display(effective_cloud_state, passkey_gate_visible);
    let voice_space_class = if passkey_gate_visible {
        "voice-space voice-space--passkey"
    } else {
        "voice-space"
    };
    let stage_class = if passkey_gate_visible {
        "conversation-stage conversation-stage--passkey"
    } else {
        "conversation-stage"
    };
    let voice_heading_id = if passkey_gate_visible {
        "passkey-entry-heading"
    } else {
        "voice-heading"
    };
    let orb_class = format!("voice-orb {}", state_snapshot.class_name());

    rsx! {
        div { class: "ambient-shell",
            div { class: "aurora aurora--one", aria_hidden: "true" }
            div { class: "aurora aurora--two", aria_hidden: "true" }
            div { class: "noise-field", aria_hidden: "true" }

            header { class: "topline",
                a {
                    class: "identity",
                    href: "#conversation",
                    aria_label: "コタエーAI 会話画面",
                    span { class: "identity__mark", "K" }
                    span { class: "identity__type",
                        strong { "KOTAE" }
                        small { "あなたが話すための音声AI" }
                    }
                }
                div { class: displayed_cloud_state.class_name(),
                    span { class: "cloud-pill__dot", aria_hidden: "true" }
                    {displayed_cloud_state.label()}
                }
            }

            main { id: "conversation", class: stage_class,
                section {
                    class: voice_space_class,
                    aria_labelledby: voice_heading_id,
                    "data-voice-state": state_snapshot.class_name(),

                    div { class: "context-line",
                        span {
                            id: "passkey-setup-status",
                            class: "passkey-setup-status",
                            role: "status",
                            aria_live: "polite",
                            aria_atomic: "true",
                            if let Some(PasskeySetupFeedback::Success(message)) = passkey_setup_feedback_snapshot {
                                {message}
                            }
                        }
                        if passkey_gate_visible {
                            span {
                                class: "context-chip",
                                "パスキーでアカウント操作を確認"
                            }
                            span {
                                class: "context-chip context-chip--quiet",
                                "声の本人確認ではない"
                            }
                        }
                        if strict_mode {
                            span {
                                class: "context-chip",
                                "STRICT / FAIL-CLOSED"
                            }
                            span {
                                class: "context-chip context-chip--quiet",
                                "PDF・検索・会話状態なし"
                            }
                        }
                        if !detected_domain.read().is_empty() {
                            span { class: "context-chip",
                                "CONTEXT / "
                                {detected_domain.read().clone()}
                            }
                        } else {
                            span { class: "context-chip context-chip--quiet", "CONTEXT / AUTO" }
                        }
                        if coach_snapshot.is_active() {
                            span {
                                class: "coach-chip",
                                {coach_snapshot.status()}
                            }
                        }
                        if research_status_snapshot == ResearchStatus::Unavailable {
                            span {
                                class: "research-status-chip research-status-chip--unavailable",
                                role: "status",
                                aria_label: "論文探索は現在利用できません",
                                "RESEARCH / UNAVAILABLE"
                            }
                        }
                    }

                    if !passkey_gate_visible {
                    div { class: "orb-field",
                        div { class: "orb-orbit orb-orbit--outer", aria_hidden: "true" }
                        div { class: "orb-orbit orb-orbit--inner", aria_hidden: "true" }
                        button {
                            id: "voice-start-action",
                            class: orb_class,
                            r#type: "button",
                            aria_label: state_snapshot.orb_action(),
                            disabled: state_snapshot.orb_disabled(),
                            onclick: move |_| {
                                // Copy the state before entering the match. Matching
                                // directly on peek() keeps its read guard alive
                                // through the selected arm, which makes the
                                // RequestingPermission write panic in Wasm.
                                let current_state = *voice_state.peek();
                                match current_state {
                                    VoiceState::Ready | VoiceState::Error(_) => {
                                        passkey_setup_feedback.set(None);
                                        start_or_resume(
                                            voice_state,
                                            generation,
                                            session_state,
                                            detected_domain,
                                            route,
                                            coach_state,
                                            needs_paper,
                                            research_status,
                                            research_records,
                                            document_info,
                                            caption,
                                            turn_notice,
                                            strict_cloud_minimization,
                                        );
                                    }
                                    VoiceState::Listening | VoiceState::Paused => {}
                                    VoiceState::RequestingPermission
                                    | VoiceState::Thinking
                                    | VoiceState::Speaking => {}
                                }
                            },
                            span { class: "voice-orb__surface", aria_hidden: "true",
                                span { class: "voice-orb__wave",
                                    for index in 0..7 {
                                        i { key: "{index}" }
                                    }
                                }
                            }
                            if matches!(state_snapshot, VoiceState::Ready | VoiceState::Error(_)) {
                                span { class: "voice-orb__cta", {state_snapshot.orb_action()} }
                            }
                            span { class: "sr-only", {state_snapshot.orb_action()} }
                        }
                    }

                    div {
                        class: "voice-status",
                        role: "status",
                        aria_live: "polite",
                        aria_atomic: "true",
                        aria_busy: matches!(
                            state_snapshot,
                            VoiceState::RequestingPermission | VoiceState::Thinking
                        ),
                        p { class: "voice-status__eyebrow",
                            if state_snapshot == VoiceState::Listening && !receipt_is_visible {
                                span { class: "live-dot", aria_hidden: "true" }
                            }
                            if receipt_is_visible {
                                {receipt_snapshot.eyebrow()}
                            } else {
                                {state_snapshot.eyebrow()}
                            }
                        }
                        h1 { id: "voice-heading",
                            if receipt_is_visible {
                                {receipt_snapshot.heading()}
                            } else if state_snapshot == VoiceState::Ready {
                                {PRODUCT_PROMISE_COPY}
                            } else if matches!(
                                state_snapshot,
                                VoiceState::RequestingPermission
                                    | VoiceState::Paused
                                    | VoiceState::Error(_)
                            ) {
                                {state_snapshot.label()}
                            } else if coach_snapshot.is_active() {
                                {coach_snapshot.heading()}
                            } else {
                                {state_snapshot.active_heading()}
                            }
                        }
                        p { class: "voice-status__hint",
                            if receipt_is_visible {
                                {receipt_snapshot.hint(state_snapshot)}
                            } else if matches!(
                                state_snapshot,
                                VoiceState::Ready
                                    | VoiceState::RequestingPermission
                                    | VoiceState::Paused
                                    | VoiceState::Error(_)
                            ) {
                                {state_snapshot.hint()}
                            } else if coach_snapshot.is_active() {
                                {coach_snapshot.hint()}
                            } else {
                                {state_snapshot.active_hint()}
                            }
                        }
                        if matches!(
                            state_snapshot,
                            VoiceState::Listening | VoiceState::Thinking | VoiceState::Speaking
                        ) {
                            p { class: "voice-status__transport", {state_snapshot.hint()} }
                            }
                        if state_snapshot.session_active() {
                            if let Some(latency) = voice_start_latency_snapshot {
                                p {
                                    class: latency.class_name(),
                                    aria_label: "実質音声の返答開始時間",
                                    {latency.status()}
                                }
                            }
                        }
                    }
                    if turn_notice_snapshot.is_visible() {
                        section {
                            class: "turn-notice",
                            role: "status",
                            aria_live: "polite",
                            aria_atomic: "true",
                            strong { {turn_notice_snapshot.heading()} }
                            p { {turn_notice_snapshot.hint()} }
                        }
                    }
                    }

                    if passkey_gate_visible {
                        div { class: "passkey-gate",
                            div {
                                class: "passkey-entry",
                                p { class: "passkey-entry__eyebrow", "マイクはまだ開きません" }
                                h1 { id: "passkey-entry-heading", "うまく話す準備はいらない" }
                                if passkey_registration_recovery_required {
                                    p {
                                        class: "passkey-entry__error",
                                        role: "alert",
                                        {PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY}
                                    }
                                } else if !passkey_setup_is_busy && passkey_setup_feedback_snapshot.is_none() {
                                    if let VoiceState::Error(message) = state_snapshot {
                                        p { class: "passkey-entry__error", role: "alert", {message} }
                                    }
                                }
                                p { class: "passkey-entry__lead",
                                    "KOTAEは短く返して、あなたの次の言葉を待ちます。一言、沈黙、聞くだけから始められます。名前やメールは入力しません。"
                                }
                                div {
                                    class: "passkey-entry__actions",
                                    role: "group",
                                    aria_label: "パスキー接続を選ぶ",
                                    button {
                                        id: "new-passkey-account-action",
                                        class: "control-button is-active",
                                        r#type: "button",
                                        aria_describedby: "new-passkey-account-warning",
                                        disabled: passkey_setup_is_busy || passkey_registration_recovery_required,
                                        onclick: move |_| {
                                            if *passkey_setup_busy.peek()
                                                || passkey_registration_recovery_required
                                            {
                                                return;
                                            }
                                            passkey_setup_busy.set(true);
                                            passkey_setup_feedback.set(None);
                                            spawn(async move {
                                                let result = cloud::register_passkey_account().await;
                                                passkey_setup_busy.set(false);
                                                match result {
                                                    Ok(()) => {
                                                        let next = generation.peek().wrapping_add(1);
                                                        generation.set(next);
                                                        cloud::stop_session();
                                                        session_state.set(String::new());
                                                        detected_domain.set(String::new());
                                                        route.set(String::new());
                                                        coach_state.set(CoachState::NONE);
                                                        needs_paper.set(false);
                                                        research_status.set(ResearchStatus::None);
                                                        research_records.set(Vec::new());
                                                        document_info.set(None);
                                                        document_error.set(None);
                                                        caption.set(None);
                                                        turn_notice.set(TurnNotice::Clear);
                                                        passkey_setup_feedback.set(Some(
                                                            PasskeySetupFeedback::Success(
                                                                PASSKEY_REGISTRATION_SUCCESS_COPY,
                                                            ),
                                                        ));
                                                        cloud_status.restart();
                                                        voice_state.set(VoiceState::Ready);
                                                    }
                                                    Err(message) => passkey_setup_feedback.set(Some(
                                                        PasskeySetupFeedback::Error(message),
                                                    )),
                                                }
                                            });
                                        },
                                        span { aria_hidden: "true", "+" }
                                        if passkey_setup_is_busy {
                                            "パスキーを登録中"
                                        } else {
                                            {NEW_PASSKEY_ACCOUNT_ACTION}
                                        }
                                    }
                                    button {
                                        id: "returning-passkey-account-action",
                                        class: "control-button",
                                        r#type: "button",
                                        disabled: passkey_setup_is_busy,
                                        onclick: move |_| {
                                            if *passkey_setup_busy.peek() {
                                                return;
                                            }
                                            passkey_setup_feedback.set(None);
                                            start_or_resume(
                                                voice_state,
                                                generation,
                                                session_state,
                                                detected_domain,
                                                route,
                                                coach_state,
                                                needs_paper,
                                                research_status,
                                                research_records,
                                                document_info,
                                                caption,
                                                turn_notice,
                                                strict_cloud_minimization,
                                            );
                                        },
                                        span { aria_hidden: "true", "↻" }
                                        {RETURNING_PASSKEY_ACTION}
                                    }
                                }
                                p {
                                    id: "new-passkey-account-warning",
                                    class: "passkey-entry__warning",
                                    {SEPARATE_PASSKEY_ACCOUNT_WARNING}
                                }
                                if !passkey_registration_recovery_required {
                                    if let Some(PasskeySetupFeedback::Error(message)) = passkey_setup_feedback_snapshot {
                                        p { class: "passkey-entry__error", role: "alert", {message} }
                                    }
                                }
                            }
                        }
                    }

                    if !passkey_gate_visible {
                    section {
                        class: if state_snapshot.session_active() {
                            "capability-strip is-collapsed"
                        } else {
                            "capability-strip"
                        },
                        aria_label: "できること",
                        div { class: "capability",
                            span { "声を出す" }
                            i { aria_hidden: "true", "→" }
                            strong { {ORDINARY_CHAT_COPY} }
                        }
                        div { class: "capability",
                            span { "自分の言葉" }
                            i { aria_hidden: "true", "→" }
                            strong { {ANSWER_SUPPORT_COPY} }
                        }
                        div { class: "capability",
                            span { "今回の実感" }
                            i { aria_hidden: "true", "→" }
                            strong { {TALK_ONLY_COPY} }
                        }
                    }

                    if *needs_paper.read() && document_snapshot.is_none() {
                        p { class: "paper-request", role: "status",
                            span { "↳" }
                            "公開版はPDFを受け取りません　必要な箇所を一言で話してください"
                        }
                    }

                    nav { class: "session-controls", aria_label: "会話の操作",
                        if state_snapshot.session_active() {
                            if state_snapshot == VoiceState::Listening {
                                button {
                                    class: "control-button is-active",
                                    r#type: "button",
                                    onclick: move |_| cloud::end_turn(),
                                    span { aria_hidden: "true", "↳" }
                                    "ここで返して"
                                }
                            }
                            button {
                                class: "control-button",
                                r#type: "button",
                                onclick: move |_| {
                                    let current_state = *voice_state.peek();
                                    if current_state.session_control_reconnects() {
                                        start_or_resume(
                                            voice_state,
                                            generation,
                                            session_state,
                                            detected_domain,
                                            route,
                                            coach_state,
                                            needs_paper,
                                            research_status,
                                            research_records,
                                            document_info,
                                            caption,
                                            turn_notice,
                                            strict_cloud_minimization,
                                        );
                                    } else if current_state.session_control_takes_turn() {
                                        // This press is an explicit fresh gesture. Stop the
                                        // pending/playing reply, then immediately open a new
                                        // intentional turn so a short correction such as
                                        // 「違う」does not need to satisfy the passive 800 ms
                                        // acoustic interruption threshold.
                                        cloud::stop_session();
                                        start_or_resume(
                                            voice_state,
                                            generation,
                                            session_state,
                                            detected_domain,
                                            route,
                                            coach_state,
                                            needs_paper,
                                            research_status,
                                            research_records,
                                            document_info,
                                            caption,
                                            turn_notice,
                                            strict_cloud_minimization,
                                        );
                                    } else {
                                        let next = generation.peek().wrapping_add(1);
                                        generation.set(next);
                                        voice_state.set(VoiceState::Paused);
                                        cloud::stop_session();
                                    }
                                },
                                span {
                                    aria_hidden: "true",
                                    {state_snapshot.session_control_icon()}
                                }
                                {state_snapshot.session_control_label()}
                            }
                            button {
                                class: "control-button control-button--end",
                                r#type: "button",
                                onclick: move |_| {
                                    let next = generation.peek().wrapping_add(1);
                                    generation.set(next);
                                    voice_state.set(VoiceState::Ready);
                                    session_state.set(String::new());
                                    detected_domain.set(String::new());
                                    route.set(String::new());
                                    coach_state.set(CoachState::NONE);
                                    needs_paper.set(false);
                                    research_status.set(ResearchStatus::None);
                                    research_records.set(Vec::new());
                                    document_info.set(None);
                                    document_error.set(None);
                                    caption.set(None);
                                    turn_notice.set(TurnNotice::Clear);
                                    cloud::stop_session();
                                },
                                span { aria_hidden: "true", "×" }
                                "終了"
                            }
                        }
                        button {
                            class: if captions_are_visible {
                                "control-button is-active"
                            } else {
                                "control-button"
                            },
                            r#type: "button",
                            aria_pressed: captions_are_visible,
                            onclick: move |_| {
                                let next = !*captions_visible.peek();
                                captions_visible.set(next);
                            },
                            span { aria_hidden: "true", "CC" }
                            if captions_are_visible { "字幕を隠す" } else { "字幕" }
                        }
                    }

                    if captions_are_visible {
                        section {
                            class: "caption-panel",
                            aria_label: "会話の字幕",
                            aria_live: "polite",
                            p { class: "caption-panel__speaker", "KOTAE / AUDIO" }
                            p {
                                if let Some(current_caption) = caption.read().as_ref() {
                                    {current_caption.clone()}
                                } else {
                                    "字幕が届くとここにだけ表示　いつもは隠しておく"
                                }
                            }
                        }
                    }
                    }
                }

                aside { class: "utility-dock", aria_label: "資料とプライバシー",
                    if !research_snapshot.is_empty() {
                        section {
                            class: "research-panel",
                            aria_label: "論文候補 / 一次資料未検証",
                            div { class: "research-panel__heading",
                                div {
                                    p { class: "utility-index", "RESEARCH / DISCOVERY" }
                                    h2 { "論文候補" }
                                }
                                span { class: "research-panel__warning", "一次資料未検証" }
                            }
                            p { class: "research-panel__note",
                                "書誌情報から見つけた候補です。主張の正しさを確認した結果ではありません。"
                            }
                            ol { class: "research-list",
                                for (index, record) in research_snapshot.iter().enumerate() {
                                    li { key: "{record.doi}",
                                        a {
                                            class: "research-card",
                                            href: record.url.clone(),
                                            target: "_blank",
                                            rel: "noopener noreferrer",
                                            aria_label: format!(
                                                "論文候補 {}: {}（一次資料未検証）",
                                                index + 1,
                                                record.display_title(),
                                            ),
                                            strong { {record.display_title()} }
                                            span { class: "research-card__meta",
                                                span { {record.source.clone()} }
                                                if !record.published.is_empty() {
                                                    i { aria_hidden: "true", "·" }
                                                    time {
                                                        datetime: record.published.clone(),
                                                        {record.published.clone()}
                                                    }
                                                }
                                            }
                                            small { "DOI {record.doi}" }
                                        }
                                    }
                                }
                            }
                        }
                    }
                    section {
                        class: "paper-drop",
                        aria_label: "クラウド最小化モード",
                        div { class: "paper-drop__heading",
                            span { class: "utility-index", "MODE" }
                            div {
                                h2 {
                                    if strict_mode {
                                        "厳格モード ON"
                                    } else {
                                        "高速会話モード"
                                    }
                                }
                                p {
                                    if strict_mode {
                                        "検査不能も停止 / PDF・外部検索・会話状態なし"
                                    } else {
                                        {STANDARD_MODE_ROUTE_LABEL}
                                    }
                                }
                            }
                        }
                        p { class: "mode-summary",
                            if strict_mode {
                                "文字起こしと返答を二段階で検査し、確認できない時は止めます。PDF・検索・会話状態は使いません。"
                            } else {
                                "速い音声経路を使います。原音と本文はKOTAEの履歴へ保存しませんが、個人情報の除去やE2EEではありません。"
                            }
                        }
                        details { class: "mode-route",
                            summary { "処理経路の詳細" }
                            p {
                                if strict_mode {
                                    "原音はSpeech-to-Textへ送ります。文字起こしの検査に通らなければVertex AIを呼びません。Vertex AIが作った返答も別に検査し、通らなければTTS・画面・音声へ出しません。どちらのモードもE2EEや完全なPII除去ではなく、DLPにも検出漏れがあり得ます。"
                                } else {
                                    {STANDARD_MODE_ROUTE_COPY}
                                }
                            }
                        }
                        nav { class: "session-controls", aria_label: "クラウド最小化モードを切り替え",
                            button {
                                class: if strict_mode {
                                    "control-button is-active"
                                } else {
                                    "control-button"
                                },
                                r#type: "button",
                                aria_pressed: strict_mode,
                                disabled: state_snapshot != VoiceState::Ready,
                                onclick: move |_| {
                                    let next = !*strict_cloud_minimization.peek();
                                    cloud::stop_session();
                                    session_state.set(String::new());
                                    detected_domain.set(String::new());
                                    route.set(String::new());
                                    coach_state.set(CoachState::NONE);
                                    needs_paper.set(false);
                                    research_status.set(ResearchStatus::None);
                                    research_records.set(Vec::new());
                                    document_info.set(None);
                                    document_error.set(None);
                                    caption.set(None);
                                    turn_notice.set(TurnNotice::Clear);
                                    strict_cloud_minimization.set(next);
                                },
                                if strict_mode {
                                    "厳格モードを切る"
                                } else {
                                    "厳格モードを使う"
                                }
                            }
                        }
                        if state_snapshot != VoiceState::Ready {
                            p { "切り替えるには、いったん会話を終了してください。" }
                        }
                    }

                    section { class: "paper-drop", aria_label: "PDF入力の提供状況",
                        div { class: "paper-drop__heading",
                            span { class: "utility-index", "01" }
                            div {
                                h2 { "PDF入力" }
                                p { "公開版では未提供" }
                            }
                        }
                        div {
                            class: "paper-picker is-disabled",
                            aria_disabled: "true",
                            span { class: "paper-picker__icon", aria_hidden: "true", "—" }
                            span {
                                strong { "ファイルを選択しません" }
                                small { "全モードで読込・送信・推論前に拒否" }
                            }
                        }
                        if let Some(message) = *document_error.read() {
                            p { class: "document-error", role: "alert", {message} }
                        }
                    }

                    LongitudinalPanel {}

                    details { class: "privacy-fold",
                        summary {
                            span { class: "utility-index", "03" }
                            span {
                                strong { "VOICE PRIVACY" }
                                small { "いま何が送られるか" }
                            }
                            i { aria_hidden: "true" }
                        }
                        div { class: "privacy-fold__body",
                            p {
                                strong { "音声" }
                                if strict_mode {
                                    "原音はTLSでSpeech-to-Textへ送ります。文字起こしはCloud Run内の決定論的検査とregional DLPがclearの時だけVertex AIへ進みます。返答も同じ検査がclearの時だけTTS・画面・音声へ出します。原音・本文はKOTAEの会話履歴、Firestore、Cloud Storage、アプリログへ保存しません。"
                                } else {
                                    {STANDARD_VOICE_PRIVACY_COPY}
                                }
                            }
                            p {
                                strong { "PDF" }
                                if strict_mode {
                                    "厳格モードでは端末で選択・読込・送信しません。"
                                } else {
                                    "公開版では全モードで選択・読込・送信できず、APIへ直接指定しても推論前に拒否します。"
                                }
                            }
                            p {
                                strong { "接続" }
                                "初めて使う時は専用操作でパスキーを登録し、次回から音声開始前に同じパスキーでこの仮名アカウントの操作を確認します。KOTAEのブラウザコードとサーバーへ秘密鍵は送りません。これは法的身元確認でも、現在マイクで話す人の認証でもありません。Firebase Authの認証セッション情報をSDKがタブ内storageに保持します。"
                            }
                            p {
                                strong { "個人情報" }
                                if strict_mode {
                                    "文字起こしと返答をCloud Runの決定論的規則とSensitive Data Protectionで検査し、検出・検査不能なら後段へ進めません。検出箇所だけを置換して会話を続ける機能ではなく、検出漏れもあり得ます。"
                                } else {
                                    "高速会話モードの原音と文字起こしは、個人情報を除去せずVertex AIへ送ります。氏名・連絡先・credentialを話さないでください。"
                                }
                            }
                            p {
                                strong { "会話支援" }
                                {SUPPORT_BOUNDARY_COPY}
                            }
                            p {
                                strong { "話者" }
                                "パスキーは声の本人確認ではないため、いまマイクで話す人がアカウントの持ち主かは認証していません。声紋は収集せず、現行VADは同席者・テレビ・合成音声を利用者の声から識別できません。周囲の声を取り込まない環境で、自分で質問を言い直して使ってください。"
                            }
                            p {
                                strong { "長期効果" }
                                "長期的に話す力が上がるかは未実証です。追跡期間と比較条件を備えた本人参加の研究が終わるまで、効果ありとは表示しません。"
                            }
                            p {
                                strong { "外部検索" }
                                if strict_mode {
                                    "厳格モードではResearch機能を呼び出しません。"
                                } else {
                                    "「外部検索で、テーマは何々の最新論文を探して」と発話全体で明示したときだけ、その検索語をCrossrefへ送ります。通常の会話やPDFからは検索しません。氏名・連絡先・症例は検索語に入れないでください。"
                                }
                            }
                            p {
                                strong { "限界" }
                                "現在のクラウド音声処理はE2EEではありません。DLPにも検出漏れがあり得るため、完全なPII除去とも表示しません。"
                            }
                            p { class: "privacy-fold__stop",
                                "一時停止・終了で　マイクと再生をすぐ止める"
                            }
                        }
                    }
                }
            }

            footer { class: "bottomline",
                span { "うまく話す準備はいらない" }
                span { "AIは短く · あなたの言葉を待つ" }
                span { "KOTAE / 2026" }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{
        ANSWER_SUPPORT_COPY, AnswerProof, COACH_CHECKPOINT_MAX_CHARS, CloudState, CoachAction,
        CoachPhase, CoachState, NATIVE_RESPONDENT_COACH_ROUTE, NEW_PASSKEY_ACCOUNT_ACTION,
        ORDINARY_CHAT_COPY, PASSKEY_AUTHENTICATION_FAILED_COPY, PASSKEY_CANCELLED_COPY,
        PASSKEY_REGISTRATION_CANCELLED_COPY, PASSKEY_REGISTRATION_FAILED_COPY,
        PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY, PASSKEY_REQUIRED_COPY,
        PASSKEY_UNSUPPORTED_COPY, PRODUCT_PROMISE_COPY, PasskeyFocusTarget, PasskeySetupFeedback,
        RETURNING_PASSKEY_ACTION, SEPARATE_PASSKEY_ACCOUNT_WARNING, STANDARD_MODE_ROUTE_COPY,
        STANDARD_MODE_ROUTE_LABEL, STANDARD_VOICE_PRIVACY_COPY, SUPPORT_BOUNDARY_COPY,
        TALK_ONLY_COPY, TurnNotice, VoiceReceipt, VoiceStartLatency, VoiceState, VoiceTurnMode,
        cloud_state_for_display, confirmed_voice_input_state, passkey_focus_target,
        recoverable_wait_turn_code, requires_passkey_choice,
        requires_passkey_registration_recovery, session_stop_pauses, silent_recognition_miss,
        turn_mode_for_gesture_epoch, valid_answer_proof_metadata, valid_coach_checkpoint_keys,
        valid_coach_checkpoint_metadata, valid_streamed_audio_metadata, valid_voice_pause_metadata,
        valid_voice_privacy_metadata, valid_voice_receipt_metadata, validated_voice_start_latency,
    };
    use serde::{Deserialize, de::IntoDeserializer};

    fn deserialize_phase(value: &str) -> Result<CoachPhase, serde::de::value::Error> {
        CoachPhase::deserialize(value.into_deserializer())
    }

    fn deserialize_action(value: &str) -> Result<CoachAction, serde::de::value::Error> {
        CoachAction::deserialize(value.into_deserializer())
    }

    fn deserialize_answer_proof(value: &str) -> Result<AnswerProof, serde::de::value::Error> {
        AnswerProof::deserialize(value.into_deserializer())
    }

    #[test]
    fn coach_metadata_accepts_only_the_reviewed_wire_values() {
        for value in [
            "none",
            "awaiting_answer",
            "awaiting_restatement",
            "expanding",
            "complete",
            "blocked",
        ] {
            assert!(deserialize_phase(value).is_ok(), "phase: {value}");
        }
        for value in [
            "none", "elicit", "restate", "expand", "complete", "retry", "release",
        ] {
            assert!(deserialize_action(value).is_ok(), "action: {value}");
        }

        assert!(deserialize_phase("scored").is_err());
        assert!(deserialize_phase("AwaitingAnswer").is_err());
        assert!(deserialize_action("answer-for-user").is_err());
        assert!(deserialize_action("Release").is_err());
    }

    #[test]
    fn coach_copy_keeps_the_answer_with_the_user() {
        let awaiting = CoachState::from_result(CoachPhase::AwaitingAnswer, CoachAction::Elicit);
        let restating =
            CoachState::from_result(CoachPhase::AwaitingRestatement, CoachAction::Restate);
        let expanding = CoachState::from_result(CoachPhase::Expanding, CoachAction::Expand);
        let complete = CoachState::from_result(CoachPhase::Complete, CoachAction::Complete);

        assert_eq!(awaiting.status(), "あなたの一言を待っています");
        assert_eq!(restating.heading(), "そこまで、ちゃんと聞こえています");
        assert_eq!(
            expanding.hint(),
            "答えなくても大丈夫　話したい方へ続けられます"
        );
        assert_eq!(complete.status(), "あなたの言葉を受け取りました");
        assert_eq!(complete.heading(), "あなた自身の言葉が出ました");
        assert!(complete.hint().contains("今の一回"));
        assert!(
            complete
                .hint()
                .contains("長期の変化を表すものではありません")
        );
        assert!(!complete.status().contains("聞かれたこと"));
        assert!(!complete.heading().contains("聞かれたこと"));

        for copy in [
            awaiting.status(),
            awaiting.heading(),
            awaiting.hint(),
            restating.status(),
            restating.heading(),
            restating.hint(),
            expanding.status(),
            expanding.heading(),
            expanding.hint(),
            complete.status(),
            complete.heading(),
            complete.hint(),
        ] {
            assert!(!copy.contains("採点"));
            assert!(!copy.contains("正解"));
            assert!(!copy.contains("不正解"));
            assert!(!copy.contains("努力"));
            assert!(!copy.contains("普通は"));
            assert!(!copy.contains("やり直し"));
        }
    }

    #[test]
    fn answer_proof_is_fail_closed_and_current_turn_only() {
        let verified = AnswerProof::QuestionBoundInputAnswerFirst;
        assert!(valid_answer_proof_metadata(
            verified,
            "respondent",
            "restructure",
            CoachPhase::Complete,
            CoachAction::Complete,
            false,
            false,
        ));
        assert!(valid_answer_proof_metadata(
            verified,
            "respondent",
            "restructure",
            CoachPhase::Expanding,
            CoachAction::Expand,
            false,
            false,
        ));
        for (target, stage, phase, action) in [
            ("assistant", "none", CoachPhase::None, CoachAction::None),
            (
                "respondent",
                "awaiting_answer",
                CoachPhase::AwaitingAnswer,
                CoachAction::Elicit,
            ),
            (
                "respondent",
                "restructure",
                CoachPhase::Complete,
                CoachAction::Release,
            ),
        ] {
            assert!(!valid_answer_proof_metadata(
                verified, target, stage, phase, action, false, false,
            ));
        }
        assert!(!valid_answer_proof_metadata(
            verified,
            "respondent",
            "restructure",
            CoachPhase::Complete,
            CoachAction::Complete,
            true,
            false,
        ));
        assert!(!valid_answer_proof_metadata(
            verified,
            "respondent",
            "restructure",
            CoachPhase::Complete,
            CoachAction::Complete,
            false,
            true,
        ));
        assert!(valid_answer_proof_metadata(
            AnswerProof::None,
            "assistant",
            "none",
            CoachPhase::None,
            CoachAction::None,
            true,
            true,
        ));
        assert!(deserialize_answer_proof("none").is_ok());
        assert!(deserialize_answer_proof("question_bound_input_answer_first").is_ok());
        assert!(deserialize_answer_proof("verified").is_err());
        assert!(deserialize_answer_proof("question_bound_user_answer_first").is_err());

        let state = CoachState::from_authoritative_result(
            CoachPhase::Complete,
            CoachAction::Complete,
            verified,
        );
        assert_eq!(state.status(), "回答所有権 / AI発話なし");
        assert_eq!(state.heading(), "あなたのAが先に出たので、AIは黙りました");
        for boundary in [
            "今回の入力だけを報告された問いへ束縛",
            "話者",
            "ライブネス",
            "外部で実際にその問いを聞かれた事実",
            "正解",
            "能力",
            "上達",
            "発話権を返しました",
        ] {
            assert!(state.hint().contains(boundary));
        }
        assert!(!state.status().contains("本人"));
        assert!(!state.heading().contains("実際に聞かれた"));
        let cleared = state.without_answer_proof();
        assert_eq!(cleared.answer_proof, AnswerProof::None);
        assert_eq!(cleared.phase, CoachPhase::Complete);
        assert_eq!(cleared.action, CoachAction::Complete);
        assert_ne!(cleared.heading(), state.heading());

        let expanding = CoachState::from_authoritative_result(
            CoachPhase::Expanding,
            CoachAction::Expand,
            verified,
        );
        assert_eq!(expanding.status(), "今回の入力 / A先頭確認");
        assert_eq!(
            expanding.heading(),
            "報告された問いへの入力が、Aから始まりました"
        );
        assert!(!expanding.hint().contains("AIは黙りました"));
    }

    #[test]
    fn confirmed_voice_input_event_can_only_clear_the_current_proof() {
        let verified = CoachState::from_authoritative_result(
            CoachPhase::Expanding,
            CoachAction::Expand,
            AnswerProof::QuestionBoundInputAnswerFirst,
        );
        let exact_keys = ["version".to_string()];
        let cleared = confirmed_voice_input_state(verified, 1.0, &exact_keys).unwrap();
        assert_eq!(cleared.answer_proof, AnswerProof::None);
        assert_eq!(cleared.phase, verified.phase);
        assert_eq!(cleared.action, verified.action);

        for (version, keys) in [
            (0.0, vec!["version".to_string()]),
            (2.0, vec!["version".to_string()]),
            (1.0, Vec::new()),
            (1.0, vec!["proof".to_string()]),
            (1.0, vec!["version".to_string(), "answerProof".to_string()]),
        ] {
            assert_eq!(confirmed_voice_input_state(verified, version, &keys), None);
        }
    }

    #[test]
    fn visible_copy_keeps_answer_support_optional_unscored_and_non_clinical() {
        let ready_hint = VoiceState::Ready.hint();

        assert!(ready_hint.contains("こんにちは"));
        assert!(ready_hint.contains("聞くだけ"));
        assert!(ready_hint.contains("沈黙"));
        assert_eq!(PRODUCT_PROMISE_COPY, "AIが話すより、あなたが話せるために");
        assert_eq!(
            ORDINARY_CHAT_COPY,
            "「こんにちは」だけで、次の一言を一緒に見つける"
        );
        assert_eq!(
            ANSWER_SUPPORT_COPY,
            "「一問だけ手伝って」で、本人のAを確認できたらAIが黙って発話権を返す"
        );
        assert_eq!(TALK_ONLY_COPY, "届いた瞬間だけ知らせて、点数にはしない");

        for boundary in [
            "KOTAEが短い話題を持ち",
            "短い返事・相づち・まとまらない長話を失敗扱いしません",
            "AIが長く話すより利用者の次の一言を優先",
            "訓練や採点を前面に出さず",
            "中心を先に",
            "外出・学校・仕事・家族",
            "勝手に目標にしません",
            "通常の受領表示",
            "入力内に報告された問い",
            "QBA Proof",
            "話者、ライブネス、外部で実際にその問いを聞かれた事実",
            "正解、能力、上達",
            "他場面への転移",
        ] {
            assert!(SUPPORT_BOUNDARY_COPY.contains(boundary), "{boundary}");
        }
        assert!(!SUPPORT_BOUNDARY_COPY.contains("曝露"));
    }

    #[test]
    fn standard_privacy_copy_discloses_native_handoff_and_pdf_rejection() {
        for copy in [STANDARD_MODE_ROUTE_COPY, STANDARD_VOICE_PRIVACY_COPY] {
            assert!(copy.contains("入力字幕"));
            assert!(copy.contains("QBA Proof"));
            assert!(copy.contains("質問"));
            assert!(copy.contains("Native"));
            assert!(copy.contains("再認識"));
            assert!(copy.contains("us-central1"));
            assert!(copy.contains("PDF入力"));
            assert!(copy.contains("推論前に拒否"));
            assert!(copy.contains("話者"));
            assert!(copy.contains("ライブネス"));
            assert!(copy.contains("正解"));
            assert!(copy.contains("能力"));
            assert!(copy.contains("上達"));
        }
        assert_eq!(
            STANDARD_MODE_ROUTE_LABEL,
            "通常会話はNative Audio / 明示した回答支援はQ-ARC + QBA Proof"
        );
        assert!(STANDARD_MODE_ROUTE_COPY.contains("人に聞かれた質問への回答支援"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("Native生成音声を破棄"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("再認識せず"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("Q-ARC"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("controller"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("完全一致"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("AIはAを作らず"));
        assert!(STANDARD_VOICE_PRIVACY_COPY.contains("AIは本人より先にAを言いません"));
        assert!(STANDARD_VOICE_PRIVACY_COPY.contains("固定長posterior"));
        assert!(STANDARD_VOICE_PRIVACY_COPY.contains("具体的な質問・答え・文字起こし・診断"));
        assert!(!STANDARD_MODE_ROUTE_COPY.contains("汎用の署名済みcheckpoint"));
        assert!(STANDARD_MODE_ROUTE_COPY.contains("Native接続不能時だけ東京リージョンSTT"));
        assert!(STANDARD_VOICE_PRIVACY_COPY.contains("保存しません"));
    }

    #[test]
    fn coach_checkpoint_metadata_is_exact_bounded_and_content_free() {
        assert!(valid_coach_checkpoint_keys(&[
            "assistanceTarget".to_string(),
            "coachAction".to_string(),
            "coachPhase".to_string(),
            "respondentStage".to_string(),
            "route".to_string(),
            "sessionState".to_string(),
            "version".to_string(),
        ]));
        for invalid in [
            vec!["sessionState".to_string()],
            vec![
                "assistanceTarget".to_string(),
                "coachAction".to_string(),
                "coachPhase".to_string(),
                "respondentStage".to_string(),
                "route".to_string(),
                "sessionState".to_string(),
                "version".to_string(),
                "extra".to_string(),
            ],
        ] {
            assert!(!valid_coach_checkpoint_keys(&invalid));
        }
        let valid = |session_state: &str,
                     route: &str,
                     assistance_target: &str,
                     respondent_stage: &str,
                     phase: CoachPhase,
                     action: CoachAction,
                     version: f64,
                     fields: u32| {
            valid_coach_checkpoint_metadata(
                session_state,
                route,
                assistance_target,
                respondent_stage,
                phase,
                action,
                version,
                fields,
            )
        };
        assert!(valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            7,
        ));
        assert!(valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "restructure",
            CoachPhase::Expanding,
            CoachAction::Expand,
            1.0,
            7,
        ));
        for invalid in [
            "",
            " signed-checkpoint",
            "signed-checkpoint ",
            "signed\ncheckpoint",
            "signed\u{007f}checkpoint",
            "signed\u{0085}checkpoint",
        ] {
            assert!(!valid(
                invalid,
                NATIVE_RESPONDENT_COACH_ROUTE,
                "respondent",
                "awaiting_answer",
                CoachPhase::AwaitingAnswer,
                CoachAction::Elicit,
                1.0,
                7,
            ));
        }
        assert!(!valid(
            &"x".repeat(COACH_CHECKPOINT_MAX_CHARS + 1),
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            "native-audio",
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "assistant",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "none",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Retry,
            1.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            2.0,
            7,
        ));
        assert!(!valid(
            "signed-checkpoint",
            NATIVE_RESPONDENT_COACH_ROUTE,
            "respondent",
            "awaiting_answer",
            CoachPhase::AwaitingAnswer,
            CoachAction::Elicit,
            1.0,
            8,
        ));
    }

    #[test]
    fn voice_receipt_is_content_free_bounded_and_never_claims_understanding() {
        assert!(valid_voice_receipt_metadata("received", 1.0, 2));
        assert!(valid_voice_receipt_metadata("clear", 1.0, 2));
        assert!(!valid_voice_receipt_metadata("understood", 1.0, 2));
        assert!(!valid_voice_receipt_metadata("received", 2.0, 2));
        assert!(!valid_voice_receipt_metadata("received", 1.0, 3));

        let receipt = VoiceReceipt::Received;
        assert!(receipt.is_visible_for(VoiceState::Listening));
        assert!(receipt.is_visible_for(VoiceState::Thinking));
        assert!(!receipt.is_visible_for(VoiceState::Speaking));
        assert_eq!(receipt.heading(), "ここまで届いています");
        for copy in [
            receipt.eyebrow(),
            receipt.heading(),
            receipt.hint(VoiceState::Listening),
            receipt.hint(VoiceState::Thinking),
        ] {
            assert!(!copy.contains("理解"));
            assert!(!copy.contains("分かった"));
            assert!(!copy.contains("伝わった"));
            assert!(!copy.contains("採点"));
        }
    }

    #[test]
    fn voice_start_latency_is_exact_current_turn_metadata() {
        let keys = ["milliseconds".to_string(), "version".to_string()];
        let on_target = validated_voice_start_latency(Some(842.4), 1.0, &keys)
            .expect("valid event")
            .expect("measured event");
        assert_eq!(on_target.milliseconds, 842);
        assert!(on_target.is_on_target());
        assert_eq!(on_target.status(), "返答開始 約0.8秒 / 1秒目標内");

        let over_target = validated_voice_start_latency(Some(1_249.7), 1.0, &keys)
            .expect("valid event")
            .expect("measured event");
        assert_eq!(over_target.milliseconds, 1_250);
        assert!(!over_target.is_on_target());
        assert_eq!(over_target.status(), "返答開始 約1.2秒 / さらに短縮中");

        assert_eq!(
            validated_voice_start_latency(None, 1.0, &keys),
            Some(None),
            "a new turn clears the prior measurement",
        );
        for invalid in [
            validated_voice_start_latency(Some(-0.1), 1.0, &keys),
            validated_voice_start_latency(Some(f64::NAN), 1.0, &keys),
            validated_voice_start_latency(
                Some(VoiceStartLatency::MAXIMUM_EVENT_MS + 0.1),
                1.0,
                &keys,
            ),
            validated_voice_start_latency(Some(500.0), 2.0, &keys),
            validated_voice_start_latency(
                Some(500.0),
                1.0,
                &[
                    "milliseconds".to_string(),
                    "transcript".to_string(),
                    "version".to_string(),
                ],
            ),
        ] {
            assert_eq!(invalid, None);
        }
    }

    #[test]
    fn passkey_entry_copy_separates_returning_authentication_from_new_registration() {
        assert_eq!(RETURNING_PASSKEY_ACTION, "登録済みの方　同じパスキーで戻る");
        assert_eq!(
            NEW_PASSKEY_ACCOUNT_ACTION,
            "初めての方　新しい仮名アカウントを作る"
        );
        assert!(SEPARATE_PASSKEY_ACCOUNT_WARNING.contains("既存の仮名アカウントとは別"));
        assert!(SEPARATE_PASSKEY_ACCOUNT_WARNING.contains("自動登録はしません"));
        assert!(!RETURNING_PASSKEY_ACTION.contains("登録する"));
        assert!(PASSKEY_AUTHENTICATION_FAILED_COPY.contains("登録が未完了"));
        assert!(PASSKEY_AUTHENTICATION_FAILED_COPY.contains("新しい仮名アカウントを作る"));
        assert!(PASSKEY_REGISTRATION_FAILED_COPY.contains("端末のパスキー設定"));
        assert!(PASSKEY_REGISTRATION_CANCELLED_COPY.contains("登録は完了しませんでした"));
        assert!(PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY.contains("新規登録を繰り返さず"));
        assert!(PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY.contains(RETURNING_PASSKEY_ACTION));
        assert!(!PASSKEY_CANCELLED_COPY.contains(NEW_PASSKEY_ACCOUNT_ACTION));
        assert_ne!(
            PASSKEY_AUTHENTICATION_FAILED_COPY,
            PASSKEY_REGISTRATION_FAILED_COPY
        );
        assert!(NEW_PASSKEY_ACCOUNT_ACTION.starts_with("初めての方"));
    }

    #[test]
    fn passkey_choice_blocks_voice_until_account_access_is_confirmed() {
        assert!(requires_passkey_choice(
            CloudState::IdentityRequired,
            VoiceState::Ready
        ));
        assert!(requires_passkey_choice(
            CloudState::PasskeyRequired,
            VoiceState::Error("cancelled")
        ));
        assert!(!requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Ready
        ));
        assert!(requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Error(PASSKEY_CANCELLED_COPY)
        ));
        assert!(requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Error(PASSKEY_AUTHENTICATION_FAILED_COPY)
        ));
        assert!(requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Error(PASSKEY_REQUIRED_COPY)
        ));
        assert!(!requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Error("マイクが許可されていない")
        ));
        assert!(!requires_passkey_choice(
            CloudState::PasskeyRequired,
            VoiceState::Listening
        ));
        assert!(requires_passkey_choice(
            CloudState::PasskeyRegistrationRecoveryRequired,
            VoiceState::Ready
        ));
        assert!(requires_passkey_choice(
            CloudState::Ready,
            VoiceState::Error(PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY)
        ));
        assert!(requires_passkey_registration_recovery(
            CloudState::Ready,
            VoiceState::Error(PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY),
            None,
        ));
        assert!(requires_passkey_registration_recovery(
            CloudState::PasskeyRegistrationRecoveryRequired,
            VoiceState::Ready,
            None,
        ));
        assert!(requires_passkey_registration_recovery(
            CloudState::Ready,
            VoiceState::Ready,
            Some(PasskeySetupFeedback::Error(
                PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY
            )),
        ));
        assert!(!requires_passkey_registration_recovery(
            CloudState::Ready,
            VoiceState::Ready,
            None,
        ));
        assert_eq!(
            cloud_state_for_display(CloudState::Ready, true),
            CloudState::PasskeyRequired
        );
        assert_eq!(
            cloud_state_for_display(CloudState::Ready, false),
            CloudState::Ready
        );
        assert_eq!(
            passkey_focus_target(
                VoiceState::Error(PASSKEY_AUTHENTICATION_FAILED_COPY),
                None,
                CloudState::Ready,
            ),
            Some(PasskeyFocusTarget::ReturningAccount)
        );
        assert_eq!(
            passkey_focus_target(
                VoiceState::Error(PASSKEY_UNSUPPORTED_COPY),
                None,
                CloudState::Ready,
            ),
            Some(PasskeyFocusTarget::ReturningAccount)
        );
        for feedback in [
            PASSKEY_REGISTRATION_FAILED_COPY,
            PASSKEY_REGISTRATION_CANCELLED_COPY,
            PASSKEY_UNSUPPORTED_COPY,
        ] {
            assert_eq!(
                passkey_focus_target(
                    VoiceState::Ready,
                    Some(PasskeySetupFeedback::Error(feedback)),
                    CloudState::PasskeyRequired,
                ),
                Some(PasskeyFocusTarget::NewAccount)
            );
        }
        assert_eq!(
            passkey_focus_target(
                VoiceState::Ready,
                Some(PasskeySetupFeedback::Error(
                    PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY
                )),
                CloudState::PasskeyRequired,
            ),
            Some(PasskeyFocusTarget::ReturningAccount)
        );
        assert_eq!(
            passkey_focus_target(
                VoiceState::Ready,
                Some(PasskeySetupFeedback::Success(
                    PASSKEY_REGISTRATION_FAILED_COPY
                )),
                CloudState::Ready,
            ),
            Some(PasskeyFocusTarget::VoiceStart)
        );
        assert_eq!(
            passkey_focus_target(
                VoiceState::Ready,
                None,
                CloudState::PasskeyRegistrationRecoveryRequired,
            ),
            Some(PasskeyFocusTarget::ReturningAccount)
        );
        assert_eq!(
            passkey_focus_target(VoiceState::Ready, None, CloudState::Ready),
            None
        );
    }

    #[test]
    fn blocked_retry_and_release_have_distinct_non_coercive_outcomes() {
        let retry = CoachState::from_result(CoachPhase::Blocked, CoachAction::Retry);
        let release = CoachState::from_result(CoachPhase::Blocked, CoachAction::Release);

        assert_eq!(retry.status(), "急がせず待っています");
        assert!(retry.hint().contains("そのまま先へ進めます"));
        assert_eq!(release.status(), "話したい方へ戻れます");
        assert_eq!(release.heading(), "そのまま話して大丈夫です");
        assert_eq!(release.hint(), "この続きでも、別の話でも大丈夫");
        assert_eq!(CoachState::NONE.phase, CoachPhase::None);
        assert_eq!(CoachState::NONE.action, CoachAction::None);
    }

    #[test]
    fn only_unfinished_coach_steps_require_the_staged_route() {
        for state in [
            CoachState::from_result(CoachPhase::AwaitingAnswer, CoachAction::Elicit),
            CoachState::from_result(CoachPhase::AwaitingRestatement, CoachAction::Restate),
            CoachState::from_result(CoachPhase::Expanding, CoachAction::Expand),
            CoachState::from_result(CoachPhase::Blocked, CoachAction::Retry),
        ] {
            assert!(state.is_active());
            assert!(state.requires_staged_route());
        }

        for state in [
            CoachState::NONE,
            CoachState::from_result(CoachPhase::Complete, CoachAction::Complete),
            CoachState::from_result(CoachPhase::Blocked, CoachAction::Release),
        ] {
            assert!(!state.requires_staged_route());
        }
    }

    #[test]
    fn only_a_fresh_gesture_can_create_intentional_authority() {
        assert!(matches!(
            turn_mode_for_gesture_epoch(true),
            VoiceTurnMode::Intentional
        ));
        assert!(matches!(
            turn_mode_for_gesture_epoch(false),
            VoiceTurnMode::Foreground
        ));
    }

    #[test]
    fn external_session_stops_pause_active_states_but_never_replace_ready() {
        assert!(!session_stop_pauses(VoiceState::Ready));
        assert!(!session_stop_pauses(VoiceState::Paused));
        assert!(session_stop_pauses(VoiceState::RequestingPermission));
        assert!(session_stop_pauses(VoiceState::Listening));
        assert!(session_stop_pauses(VoiceState::Thinking));
        assert!(session_stop_pauses(VoiceState::Speaking));
        assert!(session_stop_pauses(VoiceState::Error("temporary")));
    }

    #[test]
    fn pause_notifications_accept_only_fixed_content_free_metadata() {
        for reason in ["idle", "maximum", "hidden", "pagehide", "microphone_lost"] {
            assert!(valid_voice_pause_metadata(reason, 1.0, 2), "{reason}");
        }

        assert!(!valid_voice_pause_metadata("request_cancelled", 1.0, 2));
        assert!(!valid_voice_pause_metadata("idle", 2.0, 2));
        assert!(!valid_voice_pause_metadata("idle", 1.0, 3));
        assert!(!valid_voice_pause_metadata("", 1.0, 2));
    }

    #[test]
    fn only_server_authenticated_silent_recognition_routes_trigger_no_op_rearm() {
        assert!(silent_recognition_miss("stt-silent-no-speech"));
        assert!(silent_recognition_miss("stt-silent-low-confidence"));
        assert!(!silent_recognition_miss("stt-clarify-no-speech"));
        assert!(!silent_recognition_miss("fast"));
    }

    #[test]
    fn streamed_audio_metadata_accepts_spoken_and_silent_final_shapes() {
        assert!(valid_streamed_audio_metadata("", "audio/L16", true));
        assert!(valid_streamed_audio_metadata("", "", false));
        assert!(!valid_streamed_audio_metadata("", "audio/L16", false));
        assert!(!valid_streamed_audio_metadata("", "", true));
        assert!(!valid_streamed_audio_metadata("YQ==", "audio/L16", true));
    }

    #[test]
    fn privacy_status_is_bound_to_the_selected_mode() {
        assert!(valid_voice_privacy_metadata(
            false,
            "",
            "ordinary-state",
            super::ResearchStatus::NeedsPrimaryEvidence,
            1,
        ));
        assert!(valid_voice_privacy_metadata(
            true,
            "clear",
            "",
            super::ResearchStatus::None,
            0,
        ));
        assert!(valid_voice_privacy_metadata(
            true,
            "blocked",
            "",
            super::ResearchStatus::None,
            0,
        ));
        assert!(!valid_voice_privacy_metadata(
            true,
            "",
            "",
            super::ResearchStatus::None,
            0,
        ));
        assert!(!valid_voice_privacy_metadata(
            true,
            "clear",
            "leaked-state",
            super::ResearchStatus::None,
            0,
        ));
        assert!(!valid_voice_privacy_metadata(
            false,
            "clear",
            "",
            super::ResearchStatus::None,
            0,
        ));
    }

    #[test]
    fn voice_error_exposes_the_specific_message_and_retry_action() {
        let state = VoiceState::Error("マイクが許可されていない");

        assert_eq!(state.label(), "マイクが許可されていない");
        assert_eq!(state.eyebrow(), "接続を続けられませんでした");
        assert_eq!(
            state.hint(),
            "丸いボタンか下の「もう一度接続する」からやり直せる"
        );
        assert!(state.session_control_reconnects());
        assert_eq!(state.session_control_label(), "もう一度接続する");
        assert_eq!(state.session_control_icon(), "↻");
    }

    #[test]
    fn paused_and_active_session_controls_keep_distinct_actions() {
        assert!(VoiceState::Paused.session_control_reconnects());
        assert_eq!(VoiceState::Paused.session_control_label(), "再開");
        assert!(!VoiceState::Listening.session_control_reconnects());
        assert_eq!(VoiceState::Listening.session_control_label(), "一時停止");
        assert_eq!(VoiceState::Thinking.session_control_label(), "今話す");
        assert_eq!(VoiceState::Speaking.session_control_label(), "今話す");
        assert!(VoiceState::Thinking.session_control_takes_turn());
        assert!(VoiceState::Speaking.session_control_takes_turn());
        assert!(!VoiceState::Listening.session_control_takes_turn());
    }

    #[test]
    fn turn_notice_stays_visible_without_captions_and_never_blames_the_user() {
        assert!(!TurnNotice::Clear.is_visible());

        for notice in [
            TurnNotice::CaptureSkipped,
            TurnNotice::ReplyUnavailable,
            TurnNotice::PrivacyBlocked,
        ] {
            assert!(notice.is_visible());
            assert!(!notice.heading().is_empty());
            assert!(!notice.hint().is_empty());
            assert!(!notice.heading().contains("失敗"));
            assert!(!notice.hint().contains("不正解"));
            assert!(!notice.hint().contains("採点"));
        }

        assert!(
            TurnNotice::CaptureSkipped
                .hint()
                .contains("言い方の問題ではありません")
        );
        assert!(TurnNotice::ReplyUnavailable.heading().contains("返事だけ"));
        assert!(
            TurnNotice::ReplyUnavailable
                .hint()
                .contains("言い直さなくて大丈夫")
        );
    }

    #[test]
    fn only_an_oversized_local_capture_is_recoverable_while_waiting() {
        assert!(recoverable_wait_turn_code(Some("voice_turn_too_large")));
        for code in [
            "authentication_failed",
            "microphone_unavailable",
            "request_cancelled",
            "session_expired",
            "voice_turn_invalid",
        ] {
            assert!(!recoverable_wait_turn_code(Some(code)), "{code}");
        }
        assert!(!recoverable_wait_turn_code(None));
    }
}
