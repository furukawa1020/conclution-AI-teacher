use dioxus::prelude::*;
use serde::Deserialize;

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
            Self::Ready => "相手の質問をあなたが言い直して　まとまらないまま自分の考えを話すだけ",
            Self::RequestingPermission => "この会話に使うマイクを選ぶ",
            Self::Listening => "話し終えて約一秒　そのまま自動で返す",
            Self::Thinking => {
                "答えを組み立てている間はマイクへ送らない　返事が始まれば話して止められる"
            }
            Self::Speaking => {
                "返事を止めて訂正できる　マイクは端末内で割り込みだけを判定"
            }
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
    ConfigurationRequired,
    Unavailable,
}

impl CloudState {
    const fn label(self) -> &'static str {
        match self {
            Self::Connecting => "SECURE LINK / …",
            Self::Ready => "SECURE LINK / READY",
            Self::ConfigurationRequired => "SECURE LINK / SETUP",
            Self::Unavailable => "SECURE LINK / OFFLINE",
        }
    }

    const fn class_name(self) -> &'static str {
        match self {
            Self::Connecting | Self::ConfigurationRequired => "cloud-pill is-pending",
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
    Message(&'static str),
}

#[cfg(target_arch = "wasm32")]
mod cloud {
    use super::{
        BridgeStatus, CloudState, DocumentInfo, FinishTurnError, TurnEnd, VoiceState,
        VoiceTurnMode, VoiceTurnResult,
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

    pub async fn wait_for_turn_end() -> Result<bool, &'static str> {
        let value = wait_for_turn_end_js().await.map_err(user_message)?;
        serde_wasm_bindgen::from_value::<TurnEnd>(value)
            .map(|result| result.has_speech)
            .map_err(|_| "マイクの状態を確認できない")
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
            Err(error) => return Err(FinishTurnError::Message(user_message(error))),
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

    pub fn stop_session() {
        let _ = stop_session_js();
    }

    fn error_code(error: JsValue) -> Option<String> {
        js_sys::Error::from(error).message().as_string()
    }

    fn user_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
            Some("voice_turn_timeout") => {
                "考えている途中で時間を使い切った　内容を変えずもう一度だけ話してみて"
            }
            Some("voice_turn_unavailable") => {
                "音声の処理で止まった　接続はできている　もう一度だけ試してみて"
            }
            Some("microphone_unsupported") => {
                "このブラウザでは音声会話を使えない　最新版でためしてみて"
            }
            Some("microphone_permission_denied") => {
                "マイクが許可されていない　ブラウザの権限を確認してみて"
            }
            Some("microphone_unavailable") => "使えるマイクが見つからない　接続を確認してみて",
            Some("no_speech") => "声を拾えなかった　少し近づいてもう一度",
            Some("authentication_failed") => "安全な接続を確認できない　もう一度ためしてみて",
            Some("app_check_not_configured") => "App Check の公開サイトキーがまだない",
            Some("voice_turn_too_large") => "少し長すぎた　短く区切ってみて",
            Some("voice_turn_invalid") => "音声を確認できない　もう一度ためしてみて",
            Some("rate_limited") => "いま少し混み合ってる　少し待って再開してみて",
            Some("request_cancelled") => "会話を一時停止した",
            Some("session_expired") => "安全のためマイクを閉じた　もう一度すぐ始められる",
            Some("audio_playback_blocked") => "声を再生できない　端末の消音設定を確認してみて",
            Some("voice_api_unavailable") => "音声エージェントを準備中　少し待ってためしてみて",
            _ => "音声エージェントにつながらない　もう一度ためしてみて",
        }
    }

    fn document_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
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
    };
    use dioxus::prelude::Signal;

    #[derive(Clone)]
    pub struct DocumentClearListener;

    pub async fn status() -> CloudState {
        CloudState::Unavailable
    }

    pub async fn begin_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
    ) -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn wait_for_turn_end() -> Result<bool, &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn finish_turn(
        _session_state: &str,
        _turn_mode: VoiceTurnMode,
    ) -> Result<VoiceTurnResult, FinishTurnError> {
        Err(FinishTurnError::Message("WebAssembly版で使ってみて"))
    }

    pub async fn attach_document(_input_id: &str) -> Result<DocumentInfo, &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub fn install_document_clear_listener(
        _document_info: Signal<Option<DocumentInfo>>,
    ) -> Option<DocumentClearListener> {
        None
    }

    pub fn install_first_audio_listener(
        _voice_state: Signal<VoiceState>,
    ) -> Option<DocumentClearListener> {
        None
    }

    pub fn install_voice_interrupted_listener(
        _voice_state: Signal<VoiceState>,
    ) -> Option<DocumentClearListener> {
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
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    mut document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
) {
    if announce_permission {
        voice_state.set(VoiceState::RequestingPermission);
    }

    spawn(async move {
        let state_snapshot = session_state.peek().clone();
        if let Err(message) = cloud::begin_turn(&state_snapshot, turn_mode).await {
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
            Err(message) => {
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
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
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
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
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
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
) {
    voice_state.set(VoiceState::Listening);
    spawn(async move {
        let has_speech = match cloud::wait_for_turn_end().await {
            Ok(has_speech) => has_speech,
            Err(message) => {
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
                needs_paper,
                research_status,
                research_records,
                document_info,
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
                needs_paper,
                research_status,
                research_records,
                document_info,
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
    mut needs_paper: Signal<bool>,
    mut research_status: Signal<ResearchStatus>,
    mut research_records: Signal<Vec<ResearchRecord>>,
    mut document_info: Signal<Option<DocumentInfo>>,
    mut caption: Signal<Option<String>>,
) {
    if *generation.peek() != operation || *voice_state.peek() != VoiceState::Listening {
        return;
    }

    let state_snapshot = session_state.peek().clone();
    let consumed_document = document_info.peek().is_some();
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
                    needs_paper,
                    research_status,
                    research_records,
                    document_info,
                    caption,
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
        if turn_mode == VoiceTurnMode::Foreground
            && silent_recognition_miss(&result.route)
        {
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
                needs_paper,
                research_status,
                research_records,
                document_info,
                caption,
            );
            return;
        }
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
                needs_paper,
                research_status,
                research_records,
                document_info,
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
            needs_paper,
            research_status,
            research_records,
            document_info,
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
    needs_paper: Signal<bool>,
    research_status: Signal<ResearchStatus>,
    research_records: Signal<Vec<ResearchRecord>>,
    document_info: Signal<Option<DocumentInfo>>,
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
        needs_paper,
        research_status,
        research_records,
        document_info,
        caption,
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

fn silent_recognition_miss(route: &str) -> bool {
    matches!(
        route,
        "stt-silent-no-speech" | "stt-silent-low-confidence"
    )
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
    let mut needs_paper = use_signal(|| false);
    let mut research_status = use_signal(|| ResearchStatus::None);
    let mut research_records = use_signal(Vec::<ResearchRecord>::new);
    let mut document_info = use_signal(|| None::<DocumentInfo>);
    let mut document_error = use_signal(|| None::<&'static str>);
    let mut caption = use_signal(|| None::<String>);
    let mut captions_visible = use_signal(|| false);
    let _document_clear_listener =
        use_hook(|| cloud::install_document_clear_listener(document_info));
    let _first_audio_listener = use_hook(|| cloud::install_first_audio_listener(voice_state));
    let _voice_interrupted_listener =
        use_hook(|| cloud::install_voice_interrupted_listener(voice_state));
    let cloud_status = use_resource(|| async { cloud::status().await });

    let state_snapshot = *voice_state.read();
    let captions_are_visible = *captions_visible.read();
    let document_snapshot = document_info.read().clone();
    let research_status_snapshot = *research_status.read();
    let research_snapshot = research_records.read().clone();
    let document_is_busy = matches!(
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
                        small { "AMBIENT REASONING VOICE" }
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
                        if !detected_domain.read().is_empty() {
                            span { class: "context-chip",
                                "CONTEXT / "
                                {detected_domain.read().clone()}
                            }
                        } else {
                            span { class: "context-chip context-chip--quiet", "CONTEXT / AUTO" }
                        }
                        if !route.read().is_empty() {
                            if route.read().starts_with("respondent-") {
                                span { class: "route-chip route-chip--answer-support",
                                    "YOUR ANSWER / REFRAMED"
                                }
                            } else {
                                span { class: "route-chip", {route.read().clone()} }
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
                                            needs_paper,
                                            research_status,
                                            research_records,
                                            document_info,
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
                                "聞かれたことの答えを、"
                                br {}
                                "あなたの言葉のまま先へ"
                            } else {
                                {state_snapshot.label()}
                            }
                        }
                        p { class: "voice-status__hint", {state_snapshot.hint()} }
                    }

                    section {
                        class: if state_snapshot.session_active() {
                            "capability-strip is-collapsed"
                        } else {
                            "capability-strip"
                        },
                        aria_label: "できること",
                        div { class: "capability",
                            span { "聞かれた" }
                            i { aria_hidden: "true", "→" }
                            strong { "あなたが言い直した質問を拾う" }
                        }
                        div { class: "capability",
                            span { "まとまらない" }
                            i { aria_hidden: "true", "→" }
                            strong { "あなたの答えをそのまま受け取る" }
                        }
                        div { class: "capability",
                            span { "答える" }
                            i { aria_hidden: "true", "→" }
                            strong { "意味を変えずAだけ前へ" }
                        }
                    }

                    if *needs_paper.read() && document_snapshot.is_none() {
                        p { class: "paper-request", role: "status",
                            span { "↳" }
                            "論文の中身まで読むなら　下からPDFを今回だけ"
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
                                            needs_paper,
                                            research_status,
                                            research_records,
                                            document_info,
                                            caption,
                                        );
                                    } else {
                                        let next = generation.peek().wrapping_add(1);
                                        generation.set(next);
                                        voice_state.set(VoiceState::Paused);
                                        document_info.set(None);
                                        document_error.set(None);
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
                                    needs_paper.set(false);
                                    research_status.set(ResearchStatus::None);
                                    research_records.set(Vec::new());
                                    document_info.set(None);
                                    caption.set(None);
                                    document_error.set(None);
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
                                h2 { "論文を、今回だけ" }
                                p { "PDF / 最大7MB / 次の応答後に参照を解除" }
                            }
                        }
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
                        if let Some(message) = *document_error.read() {
                            p { class: "document-error", role: "alert", {message} }
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
                                "発話ごとにTLSで送り　アプリの履歴には残さない。会話中は、考え中と返答再生中も訂正を受けるため端末内VADがマイクを使い、確認した割り込みだけを送ります。処理中はCloud Run・Speech-to-Text・Vertex AIが内容を扱うため、E2EEではありません。"
                            }
                            p {
                                strong { "PDF" }
                                "選んだときだけ次の応答へ添付し、応答後にブラウザ上の参照を解除します。処理中はサーバーとVertex AIが内容を扱います。"
                            }
                            p {
                                strong { "接続" }
                                "匿名セッションと正規アプリからのリクエストか毎回たしかめる"
                            }
                            p {
                                strong { "話者" }
                                "話者本人の認証・識別はしていません。周囲を聴かせ続けず、相手の質問はあなた自身が言い直してから答えてください。"
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
        VoiceState, VoiceTurnMode, silent_recognition_miss, turn_mode_for_gesture_epoch,
        valid_streamed_audio_metadata,
    };

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
}
