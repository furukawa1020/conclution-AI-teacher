mod longitudinal;

use dioxus::prelude::*;
use serde::Deserialize;

const ORDINARY_CHAT_COPY: &str = "そのままなら普通の雑談";
const ANSWER_SUPPORT_COPY: &str = "「答え方を一問だけ手伝って」";
const TALK_ONLY_COPY: &str = "「今日は話すだけ」";
const RETURNING_PASSKEY_ACTION: &str = "登録済みパスキーで戻る";
const NEW_PASSKEY_ACCOUNT_ACTION: &str = "新しい仮名アカウントを作る";
const SEPARATE_PASSKEY_ACCOUNT_WARNING: &str =
    "この登録は既存の仮名アカウントとは別のアカウントを作ります。認証失敗から自動登録はしません。";
const SUPPORT_BOUNDARY_COPY: &str = "診断や治療ではなく、苦手さを測ったり課題を課したりしません。会話を楽しめることを優先し、頼まれた時だけ短く支えます。会話内容を含まない短期の目印で質問量を控えめに調整し、点数は表示しません。長期効果はまだ実証していません。";
const STRICT_PRIVACY_BLOCKED_COPY: &str = "個人情報の可能性があるため、この発話はAIへ進めませんでした。言い直さなくて大丈夫です。厳格モードを切り替えるか、別の話題から続けられます。";

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

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct CoachState {
    phase: CoachPhase,
    action: CoachAction,
}

impl CoachState {
    const NONE: Self = Self {
        phase: CoachPhase::None,
        action: CoachAction::None,
    };

    const fn from_result(phase: CoachPhase, action: CoachAction) -> Self {
        Self { phase, action }
    }

    const fn is_active(self) -> bool {
        !matches!(self.phase, CoachPhase::None)
    }

    const fn status(self) -> &'static str {
        match self.action {
            CoachAction::Elicit => "ひとつだけ聞いています",
            CoachAction::Restate => "少しだけ聞き直します",
            CoachAction::Expand => "もう少し聞かせて",
            CoachAction::Complete => "ちゃんと届きました",
            CoachAction::Retry => "音をもう一度拾います",
            CoachAction::Release => "そのまま続けられます",
            CoachAction::None => match self.phase {
                CoachPhase::None => "",
                CoachPhase::AwaitingAnswer => "ひとつだけ聞いています",
                CoachPhase::AwaitingRestatement => "少しだけ聞き直します",
                CoachPhase::Expanding => "もう少し聞かせて",
                CoachPhase::Complete => "ちゃんと届きました",
                CoachPhase::Blocked => "そのままで大丈夫",
            },
        }
    }

    const fn heading(self) -> &'static str {
        match (self.phase, self.action) {
            (CoachPhase::Blocked, CoachAction::Release) => "そのまま話して大丈夫です",
            (CoachPhase::None, _) => "まとまらないまま、話していい",
            (CoachPhase::AwaitingAnswer, _) => "短いひと言だけでも大丈夫",
            (CoachPhase::AwaitingRestatement, _) => "そこまで、ちゃんと聞こえています",
            (CoachPhase::Expanding, _) => "もう少しだけ聞かせてください",
            (CoachPhase::Complete, _) => "今の言い方で、ちゃんと伝わりました",
            (CoachPhase::Blocked, _) => "急がなくて大丈夫です",
        }
    }

    const fn hint(self) -> &'static str {
        match (self.phase, self.action) {
            (CoachPhase::Blocked, CoachAction::Release) => "この続きでも、別の話でも大丈夫",
            (CoachPhase::None, _) => "小さな声でも、3分ほどまとまらなくても、そのままどうぞ",
            (CoachPhase::AwaitingAnswer, _) => {
                "わからない、まだ決めていない、でも会話は続けられます"
            }
            (CoachPhase::AwaitingRestatement, _) => "聞きたいところを一つだけ小さくしています",
            (CoachPhase::Expanding, _) => "答えなくても大丈夫　話したい方へ続けられます",
            (CoachPhase::Complete, _) => "今の言葉のまま　その話の中身を続けます",
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
            Self::Listening => "聴いています",
            Self::Thinking => "背景まで読んでいます",
            Self::Speaking => "言葉で返しています",
            Self::Paused => "会話を止めています",
            Self::Error(message) => message,
        }
    }

    const fn eyebrow(self) -> &'static str {
        match self {
            Self::Error(_) => "接続を続けられませんでした",
            _ => self.label(),
        }
    }

    const fn hint(self) -> &'static str {
        match self {
            Self::Ready => "小さな声でも、3分ほどまとまらなくても、そのままどうぞ",
            Self::RequestingPermission => "この会話に使うマイクを選ぶ",
            Self::Listening => "話し終わりの間を見て自動で返す　長い話は急いで切らない",
            Self::Thinking => {
                "答えを組み立てている間はマイクへ送らない　返事が始まれば話して止められる"
            }
            Self::Speaking => "返事を止めて訂正できる　マイクは端末内で割り込みだけを判定",
            Self::Paused => "マイクは止まってる　再開まで何も取り込まない",
            Self::Error(_) => "丸いボタンか下の「もう一度接続する」からやり直せる",
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

    const fn session_control_label(self) -> &'static str {
        match self {
            Self::Paused => "再開",
            Self::Error(_) => "もう一度接続する",
            _ => "一時停止",
        }
    }

    const fn session_control_icon(self) -> &'static str {
        match self {
            Self::Paused => "▶",
            Self::Error(_) => "↻",
            _ => "Ⅱ",
        }
    }
}

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
#[derive(Clone, Copy, PartialEq, Eq)]
enum CloudState {
    Connecting,
    Ready,
    IdentityRequired,
    PasskeyRequired,
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
            Self::ConfigurationRequired => "SECURE LINK / SETUP",
            Self::Unavailable => "SECURE LINK / OFFLINE",
        }
    }

    const fn class_name(self) -> &'static str {
        match self {
            Self::Connecting
            | Self::IdentityRequired
            | Self::PasskeyRequired
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
        BridgeStatus, CloudState, DocumentInfo, FinishTurnError, STRICT_PRIVACY_BLOCKED_COPY,
        TurnEnd, VoiceState, VoiceTurnMode, VoiceTurnResult, WaitTurnError,
        recoverable_finish_turn_code, recoverable_wait_turn_code, session_stop_pauses,
        valid_voice_pause_metadata,
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

    pub(super) struct FirstAudioListener {
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
        ) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = waitForTurnEnd)]
        async fn wait_for_turn_end_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = finishTurn)]
        async fn finish_turn_js(
            session_state: &str,
            turn_mode: &str,
            strict_cloud_minimization: bool,
        ) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = attachDocument)]
        async fn attach_document_js(input_id: &str) -> Result<JsValue, JsValue>;

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
    ) -> Result<(), &'static str> {
        begin_turn_js(session_state, turn_mode.as_str(), strict_cloud_minimization)
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

    pub async fn attach_document(input_id: &str) -> Result<DocumentInfo, &'static str> {
        let value = attach_document_js(input_id)
            .await
            .map_err(document_message)?;
        serde_wasm_bindgen::from_value(value).map_err(|_| "PDFの情報を確認できない")
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

    pub fn install_first_audio_listener(
        mut voice_state: Signal<VoiceState>,
    ) -> Option<Rc<FirstAudioListener>> {
        let window = web_sys::window()?;
        let callback = Closure::<dyn FnMut(web_sys::Event)>::new(move |_| {
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
            Some("identity_required") | Some("identity_verification_failed") => {
                "アカウント状態を安全に確認できませんでした　マイクは開いていません"
            }
            Some("passkey_required") => {
                "話し始めるを押して　パスキーでアカウント操作を確認してください"
            }
            Some("passkey_cancelled") => "パスキー確認は完了しませんでした　マイクは開いていません",
            Some("passkey_unsupported") => {
                "このブラウザではパスキーを確認できません　マイクは開いていません"
            }
            Some("passkey_registration_failed") | Some("passkey_authentication_failed") => {
                "パスキーを安全に確認できませんでした　マイクは開いていません"
            }
            Some("passkey_account_exists") => {
                "このタブには既存アカウントがあります　新しい別アカウントは作りませんでした"
            }
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
    fn document_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
            Some("document_privacy_blocked") => {
                "厳格モードではPDFを端末で読み込まず送信もしません　標準モードなら次の応答だけに添付できます"
            }
            Some("document_not_selected") => "PDFを選んでみて",
            Some("document_type_invalid") => "ここではPDFだけを読める",
            Some("document_too_large") => "PDFは7MBまで",
            Some("document_read_failed") => "PDFを読めなかった　別のファイルをためしてみて",
            _ => "PDFを添付できなかった",
        }
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod cloud {
    use super::{
        CloudState, DocumentInfo, FinishTurnError, VoiceState, VoiceTurnMode, VoiceTurnResult,
        WaitTurnError,
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

    pub async fn attach_document(_input_id: &str) -> Result<DocumentInfo, &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub fn install_document_clear_listener(
        _document_info: Signal<Option<DocumentInfo>>,
    ) -> Option<Listener> {
        None
    }

    pub fn install_first_audio_listener(_voice_state: Signal<VoiceState>) -> Option<Listener> {
        None
    }

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
    generation: Signal<u64>,
    session_state: Signal<String>,
    detected_domain: Signal<String>,
    route: Signal<String>,
    coach_state: Signal<CoachState>,
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    mut document_info: Signal<Option<DocumentInfo>>,
    mut caption: Signal<Option<String>>,
    strict_cloud_minimization: Signal<bool>,
) {
    if announce_permission {
        voice_state.set(VoiceState::RequestingPermission);
    }

    spawn(async move {
        let strict_snapshot = *strict_cloud_minimization.peek();
        let state_snapshot = if strict_snapshot {
            String::new()
        } else {
            session_state.peek().clone()
        };
        if let Err(message) = cloud::begin_turn(&state_snapshot, turn_mode, strict_snapshot).await {
            if *generation.peek() == operation {
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
            Err(WaitTurnError::Recoverable(message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    // The failed encoded capture has already been discarded.
                    // Keep the opaque session and wait for a fresh foreground
                    // utterance; never resend bytes from the oversized turn.
                    caption.set(Some(message.to_string()));
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
    mut caption: Signal<Option<String>>,
    strict_cloud_minimization: Signal<bool>,
) {
    voice_state.set(VoiceState::Listening);
    spawn(async move {
        let has_speech = match cloud::wait_for_turn_end().await {
            Ok(has_speech) => has_speech,
            Err(WaitTurnError::Recoverable(message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    caption.set(Some(message.to_string()));
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
                    strict_cloud_minimization,
                );
                return;
            }
            Err(FinishTurnError::Recoverable(message)) => {
                if consumed_document {
                    document_info.set(None);
                }
                // The captured turn was consumed and must never be resent. A
                // transient provider or network failure also must not revoke
                // the user's foreground microphone gesture: keep the opaque
                // pre-turn state, leave the session open, and listen for a new
                // utterance. The notice is optional caption text only; it does
                // not make the user repeat the failed turn.
                caption.set(Some(message.to_string()));
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
                strict_cloud_minimization,
            );
            return;
        }
        coach_state.set(CoachState::from_result(
            result.coach_phase,
            result.coach_action,
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
    strict_cloud_minimization: Signal<bool>,
) {
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
        strict_cloud_minimization,
    );
}

fn human_file_size(bytes: u64) -> String {
    if bytes >= 1_048_576 {
        format!("{:.1} MB", bytes as f64 / 1_048_576.0)
    } else {
        format!("{:.0} KB", bytes as f64 / 1_024.0)
    }
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
        section {
            class: "paper-drop",
            aria_label: "個人内の推移を測る任意機能",
            div { class: "paper-drop__heading",
                span { class: "utility-index", "02" }
                div {
                    h2 { "個人内の推移を測る機能は実装" }
                    p { "長期効果は未実証・比較試験ではない" }
                }
            }
            p {
                "開始時・4週目・8週目・終了4週後・終了12週後の固定質問を、各期限内に一度だけ自己記録します。"
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

#[component]
fn App() -> Element {
    let mut voice_state = use_signal(|| VoiceState::Ready);
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
    let mut captions_visible = use_signal(|| false);
    let mut strict_cloud_minimization = use_signal(|| false);
    let mut passkey_setup_busy = use_signal(|| false);
    let mut passkey_setup_notice = use_signal(|| None::<&'static str>);
    let _document_clear_listener =
        use_hook(|| cloud::install_document_clear_listener(document_info));
    let _first_audio_listener = use_hook(|| cloud::install_first_audio_listener(voice_state));
    let _voice_interrupted_listener =
        use_hook(|| cloud::install_voice_interrupted_listener(voice_state));
    let _voice_session_paused_listener =
        use_hook(|| cloud::install_voice_session_paused_listener(voice_state, generation));
    let mut cloud_status = use_resource(|| async { cloud::status().await });

    let state_snapshot = *voice_state.read();
    let coach_snapshot = *coach_state.read();
    let captions_are_visible = *captions_visible.read();
    let strict_mode = *strict_cloud_minimization.read();
    let document_snapshot = document_info.read().clone();
    let research_status_snapshot = *research_status.read();
    let research_snapshot = research_records.read().clone();
    let passkey_setup_is_busy = *passkey_setup_busy.read();
    let document_is_busy = strict_mode
        || matches!(
            state_snapshot,
            VoiceState::RequestingPermission | VoiceState::Thinking | VoiceState::Speaking
        );
    let prepared_cloud_state = cloud_status
        .read()
        .as_ref()
        .copied()
        .unwrap_or(CloudState::Connecting);
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
                        small { "話す / 考える / 伝わる" }
                    }
                }
                div { class: prepared_cloud_state.class_name(),
                    span { class: "cloud-pill__dot", aria_hidden: "true" }
                    {prepared_cloud_state.label()}
                }
            }

            main { id: "conversation", class: "conversation-stage",
                section {
                    class: "voice-space",
                    aria_labelledby: "voice-heading",
                    "data-voice-state": state_snapshot.class_name(),

                    div { class: "context-line", aria_live: "polite",
                        if matches!(
                            prepared_cloud_state,
                            CloudState::IdentityRequired | CloudState::PasskeyRequired
                        ) {
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
                                aria_label: "会話を支える次の一歩",
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

                    div { class: "orb-field",
                        div { class: "orb-orbit orb-orbit--outer", aria_hidden: "true" }
                        div { class: "orb-orbit orb-orbit--inner", aria_hidden: "true" }
                        button {
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
                        aria_busy: matches!(
                            state_snapshot,
                            VoiceState::RequestingPermission | VoiceState::Thinking
                        ),
                        p { class: "voice-status__eyebrow",
                            if state_snapshot == VoiceState::Listening {
                                span { class: "live-dot", aria_hidden: "true" }
                            }
                            {state_snapshot.eyebrow()}
                        }
                        h1 { id: "voice-heading",
                            if state_snapshot == VoiceState::Ready {
                                "まとまらないまま、"
                                br {}
                                "話していい"
                            } else if matches!(
                                state_snapshot,
                                VoiceState::RequestingPermission
                                    | VoiceState::Paused
                                    | VoiceState::Error(_)
                            ) {
                                {state_snapshot.label()}
                            } else {
                                {coach_snapshot.heading()}
                            }
                        }
                        p { class: "voice-status__hint",
                            if matches!(
                                state_snapshot,
                                VoiceState::Ready
                                    | VoiceState::RequestingPermission
                                    | VoiceState::Paused
                                    | VoiceState::Error(_)
                            ) {
                                {state_snapshot.hint()}
                            } else {
                                {coach_snapshot.hint()}
                            }
                        }
                        if matches!(
                            state_snapshot,
                            VoiceState::Listening | VoiceState::Thinking | VoiceState::Speaking
                        ) {
                            p { class: "voice-status__transport", {state_snapshot.hint()} }
                            }
                    }

                    if matches!(
                        prepared_cloud_state,
                        CloudState::IdentityRequired | CloudState::PasskeyRequired
                    ) && matches!(state_snapshot, VoiceState::Ready | VoiceState::Error(_)) {
                        section {
                            class: "passkey-entry",
                            aria_label: "パスキーで仮名アカウントへ接続",
                            p { class: "passkey-entry__lead",
                                "登録済みなら同じパスキーで戻れます。初めて使う時だけ、新しい仮名アカウントを作ってください。"
                            }
                            nav { class: "passkey-entry__actions", aria_label: "パスキー接続を選ぶ",
                                button {
                                    class: "control-button is-active",
                                    r#type: "button",
                                    disabled: passkey_setup_is_busy,
                                    onclick: move |_| {
                                        if *passkey_setup_busy.peek() {
                                            return;
                                        }
                                        passkey_setup_notice.set(None);
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
                                            strict_cloud_minimization,
                                        );
                                    },
                                    span { aria_hidden: "true", "↻" }
                                    {RETURNING_PASSKEY_ACTION}
                                }
                                button {
                                    class: "control-button",
                                    r#type: "button",
                                    disabled: passkey_setup_is_busy,
                                    onclick: move |_| {
                                        if *passkey_setup_busy.peek() {
                                            return;
                                        }
                                        passkey_setup_busy.set(true);
                                        passkey_setup_notice.set(None);
                                        spawn(async move {
                                            let result = cloud::register_passkey_account().await;
                                            passkey_setup_busy.set(false);
                                            match result {
                                                Ok(()) => {
                                                    passkey_setup_notice.set(Some(
                                                        "新しい仮名アカウントを作りました　マイクはまだ開いていません",
                                                    ));
                                                    cloud_status.restart();
                                                }
                                                Err(message) => passkey_setup_notice.set(Some(message)),
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
                            }
                            p { class: "passkey-entry__warning",
                                {SEPARATE_PASSKEY_ACCOUNT_WARNING}
                            }
                            if let Some(message) = *passkey_setup_notice.read() {
                                p { class: "passkey-entry__notice", role: "status", {message} }
                            }
                        }
                    }

                    section {
                        class: if state_snapshot.session_active() {
                            "capability-strip is-collapsed"
                        } else {
                            "capability-strip"
                        },
                        aria_label: "できること",
                        div { class: "capability",
                            span { "話す" }
                            i { aria_hidden: "true", "→" }
                            strong { {ORDINARY_CHAT_COPY} }
                        }
                        div { class: "capability",
                            span { "支援" }
                            i { aria_hidden: "true", "→" }
                            strong { {ANSWER_SUPPORT_COPY} }
                        }
                        div { class: "capability",
                            span { "戻る" }
                            i { aria_hidden: "true", "→" }
                            strong { {TALK_ONLY_COPY} }
                        }
                    }

                    if *needs_paper.read() {
                        p { class: "paper-request", role: "status",
                            span { "↳" }
                            if strict_mode {
                                "厳格モードではPDFを送りません　標準モードへ戻すと今回だけ添付できます"
                            } else {
                                "論文の中身まで読むなら　下からPDFを今回だけ"
                            }
                        }
                    }

                    nav { class: "session-controls", aria_label: "会話の操作",
                        if state_snapshot.session_active() {
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
                                        "標準モード"
                                    }
                                }
                                p {
                                    if strict_mode {
                                        "検査不能も停止 / PDF・外部検索・会話状態なし"
                                    } else {
                                        "PDF・外部検索・会話の続きが使えます"
                                    }
                                }
                            }
                        }
                        p {
                            if strict_mode {
                                "原音はSpeech-to-Textへ送ります。その後の文字起こしと返答を端末内検査とregional DLPの両方で検査し、検出・障害・時間切れならVertex AI・TTS・会話状態へ進めません。"
                            } else {
                                "通常の会話機能を使うモードです。処理中はSpeech-to-Text・Cloud Run・Vertex AI・TTSが平文を扱います。"
                            }
                        }
                        p {
                            "どちらもE2EEや完全なPII除去ではありません。厳格モードもDLPの検出漏れまでは保証できません。"
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

                    section { class: "paper-drop",
                        div { class: "paper-drop__heading",
                            span { class: "utility-index", "01" }
                            div {
                                h2 { "論文を、今回だけ" }
                                p {
                                    if strict_mode {
                                        "厳格モードでは選択・読込・送信しません"
                                    } else {
                                        "PDF / 最大7MB / 次の応答後に参照を解除"
                                    }
                                }
                            }
                        }
                        if strict_mode {
                            div {
                                class: "paper-picker is-disabled",
                                aria_disabled: "true",
                                span { class: "paper-picker__icon", aria_hidden: "true", "—" }
                                span {
                                    strong { "厳格モードではPDFを送らない" }
                                    small { "標準モードへ戻すと、次の応答だけに添付できます" }
                                }
                            }
                        } else {
                            input {
                                id: "paper-input",
                                class: "sr-only",
                                r#type: "file",
                                accept: "application/pdf,.pdf",
                                disabled: document_is_busy,
                                onchange: move |_| {
                                    document_error.set(None);
                                    spawn(async move {
                                        match cloud::attach_document("paper-input").await {
                                            Ok(info) => {
                                                document_info.set(Some(info));
                                                needs_paper.set(false);
                                            }
                                            Err(message) => document_error.set(Some(message)),
                                        }
                                    });
                                },
                            }
                            label {
                                class: if document_is_busy {
                                    "paper-picker is-disabled"
                                } else {
                                    "paper-picker"
                                },
                                r#for: "paper-input",
                                span { class: "paper-picker__icon", aria_hidden: "true", "＋" }
                                if let Some(info) = document_snapshot.as_ref() {
                                    span {
                                        strong { {info.name.clone()} }
                                        small { "{human_file_size(info.size_bytes)} / 次の応答だけに使用" }
                                    }
                                } else {
                                    span {
                                        strong { "PDFを渡す" }
                                        small { "保存せず、文脈を読む" }
                                    }
                                }
                            }
                        }
                    }

                    LongitudinalPanel {}

                    details { class: "privacy-fold", open: true,
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
                                    "原音はTLSでSpeech-to-Textへ送ります。文字起こしと返答は端末内検査とregional DLPの両方がclearの時だけ次へ進み、検出・障害・時間切れでは停止します。原音・本文はKOTAEの会話履歴、Firestore、Cloud Storage、アプリログへ保存しません。"
                                } else {
                                    "発話ごとにTLSでCloud RunとSpeech-to-Textへ送り、文字起こしをVertex AI、返答をText-to-Speechで処理します。原音・本文はKOTAEの会話履歴、Firestore、Cloud Storage、アプリログへ保存しません。"
                                }
                            }
                            p {
                                strong { "PDF" }
                                if strict_mode {
                                    "厳格モードでは端末で選択・読込・送信しません。"
                                } else {
                                    "自分で選んだ時だけ次の応答へ添付し、応答後にブラウザ上の参照を解除します。処理中はCloud RunとVertex AIが本文を扱います。"
                                }
                            }
                            p {
                                strong { "接続" }
                                "初めて使う時は専用操作でパスキーを登録し、次回から音声開始前に同じパスキーでこの仮名アカウントの操作を確認します。KOTAEのブラウザコードとサーバーへ秘密鍵は送りません。これは法的身元確認でも、現在マイクで話す人の認証でもありません。Firebase Authの認証セッション情報をSDKがタブ内storageに保持します。"
                            }
                            p {
                                strong { "個人情報" }
                                "メール・電話・長い識別子・credentialをCloud Runの決定論的規則とSensitive Data Protectionで置換し、検査不能ならVertex AIを呼びません。ただし検出器には漏れがあり得るため、完全PII除去とは表示しません。"
                            }
                            p {
                                strong { "会話支援" }
                                {SUPPORT_BOUNDARY_COPY}
                            }
                            p {
                                strong { "話者" }
                                "パスキーは声の本人確認ではないため、いまマイクで話す人がアカウントの持ち主かは認証していません。声紋は収集せず、周囲の声を自動採用しません。"
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
                span { "NO PROMPT REQUIRED" }
                span { "VOICE IN · REASONING IN BETWEEN · VOICE OUT" }
                span { "KOTAE / 2026" }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{
        ANSWER_SUPPORT_COPY, CoachAction, CoachPhase, CoachState, NEW_PASSKEY_ACCOUNT_ACTION,
        ORDINARY_CHAT_COPY, RETURNING_PASSKEY_ACTION, SEPARATE_PASSKEY_ACCOUNT_WARNING,
        SUPPORT_BOUNDARY_COPY, TALK_ONLY_COPY, VoiceState, VoiceTurnMode,
        recoverable_wait_turn_code, session_stop_pauses, silent_recognition_miss,
        turn_mode_for_gesture_epoch, valid_streamed_audio_metadata, valid_voice_pause_metadata,
        valid_voice_privacy_metadata,
    };
    use serde::{Deserialize, de::IntoDeserializer};

    fn deserialize_phase(value: &str) -> Result<CoachPhase, serde::de::value::Error> {
        CoachPhase::deserialize(value.into_deserializer())
    }

    fn deserialize_action(value: &str) -> Result<CoachAction, serde::de::value::Error> {
        CoachAction::deserialize(value.into_deserializer())
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

        assert_eq!(awaiting.status(), "ひとつだけ聞いています");
        assert_eq!(restating.heading(), "そこまで、ちゃんと聞こえています");
        assert_eq!(
            expanding.hint(),
            "答えなくても大丈夫　話したい方へ続けられます"
        );
        assert_eq!(complete.status(), "ちゃんと届きました");

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
    fn visible_copy_keeps_answer_support_optional_unscored_and_non_clinical() {
        let ready_hint = VoiceState::Ready.hint();

        assert!(ready_hint.contains("まとまらなくても"));
        assert!(ready_hint.contains("小さな声"));
        assert_eq!(ORDINARY_CHAT_COPY, "そのままなら普通の雑談");
        assert_eq!(ANSWER_SUPPORT_COPY, "「答え方を一問だけ手伝って」");
        assert_eq!(TALK_ONLY_COPY, "「今日は話すだけ」");

        for boundary in [
            "診断や治療ではなく",
            "苦手さを測ったり課題を課したりしません",
            "会話を楽しめることを優先",
            "頼まれた時だけ短く支えます",
            "会話内容を含まない短期の目印",
            "質問量を控えめに調整",
            "点数は表示しません",
            "長期効果はまだ実証していません",
        ] {
            assert!(SUPPORT_BOUNDARY_COPY.contains(boundary), "{boundary}");
        }
        assert!(!SUPPORT_BOUNDARY_COPY.contains("曝露"));
    }

    #[test]
    fn passkey_entry_copy_separates_returning_authentication_from_new_registration() {
        assert_eq!(RETURNING_PASSKEY_ACTION, "登録済みパスキーで戻る");
        assert_eq!(NEW_PASSKEY_ACCOUNT_ACTION, "新しい仮名アカウントを作る");
        assert!(SEPARATE_PASSKEY_ACCOUNT_WARNING.contains("既存の仮名アカウントとは別"));
        assert!(SEPARATE_PASSKEY_ACCOUNT_WARNING.contains("自動登録はしません"));
        assert!(!RETURNING_PASSKEY_ACTION.contains("登録する"));
    }

    #[test]
    fn blocked_retry_and_release_have_distinct_non_coercive_outcomes() {
        let retry = CoachState::from_result(CoachPhase::Blocked, CoachAction::Retry);
        let release = CoachState::from_result(CoachPhase::Blocked, CoachAction::Release);

        assert_eq!(retry.status(), "音をもう一度拾います");
        assert!(retry.hint().contains("そのまま先へ進めます"));
        assert_eq!(release.status(), "そのまま続けられます");
        assert_eq!(release.heading(), "そのまま話して大丈夫です");
        assert_eq!(release.hint(), "この続きでも、別の話でも大丈夫");
        assert_eq!(CoachState::NONE.phase, CoachPhase::None);
        assert_eq!(CoachState::NONE.action, CoachAction::None);
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
