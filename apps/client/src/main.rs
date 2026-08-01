use dioxus::prelude::*;
use serde::Deserialize;

const ORDINARY_CHAT_COPY: &str = "そのままなら普通の雑談";
const ANSWER_SUPPORT_COPY: &str = "「答え方を一問だけ手伝って」";
const TALK_ONLY_COPY: &str = "「今日は話すだけ」";
const SUPPORT_BOUNDARY_COPY: &str = "診断や治療ではありません。普段は会話の流れを優先し、「答え方を手伝って」と頼まれたときだけ短く支えます。会話内容を含まない短期の目印で質問量を調整しますが、長期効果はまだ実証していません。";

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
            (CoachPhase::None, _) => "質問でも、ぼやきでも、短い声でも、そのままどうぞ",
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
            Self::Ready => "質問でも、ぼやきでも、まとまらなくても、小さな声のままどうぞ",
            Self::RequestingPermission => "この会話に使うマイクを選ぶ",
            Self::Listening => "話し終えて約一秒　そのまま自動で返す",
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
    ConfigurationRequired,
    Unavailable,
}

impl CloudState {
    const fn label(self) -> &'static str {
        match self {
            Self::Connecting => "ACCOUNT / …",
            Self::Ready => "ACCOUNT / READY",
            Self::IdentityRequired => "IDENTITY / REQUIRED",
            Self::ConfigurationRequired => "ACCOUNT / SETUP",
            Self::Unavailable => "ACCOUNT / OFFLINE",
        }
    }

    const fn class_name(self) -> &'static str {
        match self {
            Self::Connecting | Self::IdentityRequired | Self::ConfigurationRequired => {
                "cloud-pill is-pending"
            }
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
        BridgeStatus, CloudState, FinishTurnError, TurnEnd, VoiceState, VoiceTurnMode,
        VoiceTurnResult, WaitTurnError, recoverable_finish_turn_code, recoverable_wait_turn_code,
        session_stop_pauses, valid_voice_pause_metadata,
    };
    use dioxus::prelude::{ReadableExt, Signal, WritableExt};
    use std::rc::Rc;
    use wasm_bindgen::JsCast;
    use wasm_bindgen::prelude::*;

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

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = beginTurn)]
        async fn begin_turn_js(session_state: &str, turn_mode: &str) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = waitForTurnEnd)]
        async fn wait_for_turn_end_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = finishTurn)]
        async fn finish_turn_js(session_state: &str, turn_mode: &str) -> Result<JsValue, JsValue>;

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
            "configuration-required" => CloudState::ConfigurationRequired,
            _ => CloudState::Unavailable,
        }
    }

    pub async fn begin_turn(
        session_state: &str,
        turn_mode: VoiceTurnMode,
    ) -> Result<(), &'static str> {
        begin_turn_js(session_state, turn_mode.as_str())
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
    ) -> Result<VoiceTurnResult, FinishTurnError> {
        let value = match finish_turn_js(session_state, turn_mode.as_str()).await {
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
                "話し始める前に確認済みGoogleアカウントでログインしてください"
            }
            Some("app_check_not_configured") => "App Check の公開サイトキーがまだない",
            Some("voice_turn_too_large") => "少し長すぎた　短く区切ってみて",
            Some("voice_turn_invalid") => "音声を確認できない　もう一度ためしてみて",
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

#[cfg(not(target_arch = "wasm32"))]
mod cloud {
    use super::{
        CloudState, FinishTurnError, VoiceState, VoiceTurnMode, VoiceTurnResult, WaitTurnError,
    };
    use dioxus::prelude::Signal;

    #[derive(Clone)]
    pub struct Listener;

    pub async fn status() -> CloudState {
        CloudState::Unavailable
    }

    pub async fn begin_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
    ) -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn wait_for_turn_end() -> Result<bool, WaitTurnError> {
        Err(WaitTurnError::Terminal("WebAssembly版で使ってみて"))
    }

    pub async fn finish_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
    ) -> Result<VoiceTurnResult, FinishTurnError> {
        Err(FinishTurnError::Message("WebAssembly版で使ってみて"))
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
    mut caption: Signal<Option<String>>,
) {
    if announce_permission {
        voice_state.set(VoiceState::RequestingPermission);
    }

    spawn(async move {
        let state_snapshot = session_state.peek().clone();
        if let Err(message) = cloud::begin_turn(&state_snapshot, turn_mode).await {
            if *generation.peek() == operation {
                cloud::stop_session();
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
                        caption,
                    );
                }
                return;
            }
            Err(WaitTurnError::Terminal(message)) => {
                if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
                    cloud::stop_session();
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
                    caption,
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
                    caption,
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
    mut caption: Signal<Option<String>>,
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
                        caption,
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
                caption,
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
                caption,
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
    mut caption: Signal<Option<String>>,
) {
    if *generation.peek() != operation || *voice_state.peek() != VoiceState::Listening {
        return;
    }

    let state_snapshot = session_state.peek().clone();
    research_status.set(ResearchStatus::None);
    research_records.set(Vec::new());
    voice_state.set(VoiceState::Thinking);

    spawn(async move {
        let result = cloud::finish_turn(&state_snapshot, turn_mode).await;
        if *generation.peek() != operation {
            return;
        }

        let result = match result {
            Ok(result) => result,
            Err(FinishTurnError::Interrupted) => {
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
                    caption,
                );
                return;
            }
            Err(FinishTurnError::Recoverable(message)) => {
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
                    caption,
                );
                return;
            }
            Err(FinishTurnError::Message(message)) => {
                cloud::stop_session();
                voice_state.set(VoiceState::Error(message));
                return;
            }
        };

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
                caption,
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
                caption,
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
            caption,
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
    caption: Signal<Option<String>>,
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
        caption,
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
    let mut caption = use_signal(|| None::<String>);
    let mut captions_visible = use_signal(|| false);
    let _first_audio_listener = use_hook(|| cloud::install_first_audio_listener(voice_state));
    let _voice_interrupted_listener =
        use_hook(|| cloud::install_voice_interrupted_listener(voice_state));
    let _voice_session_paused_listener =
        use_hook(|| cloud::install_voice_session_paused_listener(voice_state, generation));
    let cloud_status = use_resource(|| async { cloud::status().await });

    let state_snapshot = *voice_state.read();
    let coach_snapshot = *coach_state.read();
    let captions_are_visible = *captions_visible.read();
    let research_status_snapshot = *research_status.read();
    let research_snapshot = research_records.read().clone();
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
                        if prepared_cloud_state == CloudState::IdentityRequired {
                            span {
                                class: "context-chip",
                                "最初の一回だけ　Googleアカウントを確認"
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
                                            caption,
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
                            "PDF原本の読取りは安全な匿名化経路ができるまで停止中"
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
                                            caption,
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
                    section { class: "paper-drop",
                        div { class: "paper-drop__heading",
                            span { class: "utility-index", "01" }
                            div {
                                h2 { "PDFは、まだ送らない" }
                                p { "原本を安全に匿名化できる経路まで停止中" }
                            }
                        }
                        div {
                            class: "paper-picker is-disabled",
                            aria_disabled: "true",
                            span { class: "paper-picker__icon", aria_hidden: "true", "×" }
                            span {
                                strong { "PDF送信を停止しています" }
                                small { "ファイルを読まず、クラウドへも送りません" }
                            }
                        }
                    }

                    details { class: "privacy-fold", open: true,
                        summary {
                            span { class: "utility-index", "02" }
                            span {
                                strong { "VOICE PRIVACY" }
                                small { "いま何が送られるか" }
                            }
                            i { aria_hidden: "true" }
                        }
                        div { class: "privacy-fold__body",
                            p {
                                strong { "音声" }
                                "発話ごとにTLSでCloud RunとSpeech-to-Textへ送ります。生音声はSpeech-to-Textが、文字起こしは東京リージョンのSensitive Data Protectionが平文で処理します。保護後の文字だけをVertex AIへ渡しますが、端末間E2EEではありません。"
                            }
                            p {
                                strong { "PDF" }
                                "現在はブラウザでファイル内容を読まず、Cloud RunやVertex AIへ送りません。PDFを安全に匿名化できる隔離経路を実装するまで停止します。"
                            }
                            p {
                                strong { "接続" }
                                "確認済みGoogleアカウントと正規アプリからのリクエストか毎回たしかめる。メールアドレスはKOTAEの画面・ログ・会話状態へ保存しません。"
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
                                "Googleアカウントを使えることの確認と、いまマイクで話す人の識別は別です。声紋は収集せず、話者本人の認証はしていません。相手の質問はあなた自身が言い直してから答えてください。"
                            }
                            p {
                                strong { "長期効果" }
                                "長期的に話す力が上がるかは未実証です。追跡期間と比較条件を備えた本人参加の研究が終わるまで、効果ありとは表示しません。"
                            }
                            p {
                                strong { "外部検索" }
                                "「外部検索で、テーマは何々の最新論文を探して」と発話全体で明示したときだけ、その検索語をCrossrefへ送ります。通常の会話やPDFからは検索しません。氏名・連絡先・症例は検索語に入れないでください。"
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
        ANSWER_SUPPORT_COPY, CoachAction, CoachPhase, CoachState, ORDINARY_CHAT_COPY,
        SUPPORT_BOUNDARY_COPY, TALK_ONLY_COPY, VoiceState, VoiceTurnMode,
        recoverable_wait_turn_code, session_stop_pauses, silent_recognition_miss,
        turn_mode_for_gesture_epoch, valid_streamed_audio_metadata, valid_voice_pause_metadata,
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
            "診断や治療ではありません",
            "普段は会話の流れを優先",
            "頼まれたときだけ短く支えます",
            "会話内容を含まない短期の目印",
            "質問量を調整",
            "長期効果はまだ実証していません",
        ] {
            assert!(SUPPORT_BOUNDARY_COPY.contains(boundary), "{boundary}");
        }
        assert!(!SUPPORT_BOUNDARY_COPY.contains("曝露"));
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
