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

impl VoiceState {
    const fn label(self) -> &'static str {
        match self {
            Self::Ready => "話しはじめる",
            Self::RequestingPermission => "マイクを準備しています",
            Self::Listening => "聴いています",
            Self::Thinking => "背景まで読んでいます",
            Self::Speaking => "言葉で返しています",
            Self::Paused => "会話を止めています",
            Self::Error(_) => "接続を続けられませんでした",
        }
    }

    const fn hint(self) -> &'static str {
        match self {
            Self::Ready => "テーマも質問もいらない　ぼやきから始められる",
            Self::RequestingPermission => "この会話に使うマイクを選ぶ",
            Self::Listening => "話し終えて約一秒　そのまま自動で返す",
            Self::Thinking => "迷いと前提をほどいて　返す言葉を組み立てる",
            Self::Speaking => "返事のあと　そのまままた聴きはじめる",
            Self::Paused => "マイクは止まってる　再開まで何も取り込まない",
            Self::Error(message) => message,
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
    session_state: String,
    detected_domain: String,
    route: String,
    needs_paper: bool,
    #[serde(default)]
    caption: Option<String>,
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

#[cfg(target_arch = "wasm32")]
mod cloud {
    use super::{BridgeStatus, CloudState, DocumentInfo, TurnEnd, VoiceTurnResult};
    use wasm_bindgen::prelude::*;

    #[wasm_bindgen]
    extern "C" {
        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = getStatus)]
        async fn get_status_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = beginTurn)]
        async fn begin_turn_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = waitForTurnEnd)]
        async fn wait_for_turn_end_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = finishTurn)]
        async fn finish_turn_js(session_state: &str) -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = playResponse)]
        async fn play_response_js(
            audio_base64: &str,
            audio_mime_type: &str,
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
            "configuration-required" => CloudState::ConfigurationRequired,
            _ => CloudState::Unavailable,
        }
    }

    pub async fn begin_turn() -> Result<(), &'static str> {
        begin_turn_js().await.map(|_| ()).map_err(user_message)
    }

    pub async fn wait_for_turn_end() -> Result<bool, &'static str> {
        let value = wait_for_turn_end_js().await.map_err(user_message)?;
        serde_wasm_bindgen::from_value::<TurnEnd>(value)
            .map(|result| result.has_speech)
            .map_err(|_| "マイクの状態を確認できない")
    }

    pub async fn finish_turn(session_state: &str) -> Result<VoiceTurnResult, &'static str> {
        let value = finish_turn_js(session_state).await.map_err(user_message)?;
        serde_wasm_bindgen::from_value(value)
            .map_err(|_| "音声応答を確認できない　もう一度ためしてみて")
    }

    pub async fn play_response(
        audio_base64: &str,
        audio_mime_type: &str,
    ) -> Result<(), &'static str> {
        play_response_js(audio_base64, audio_mime_type)
            .await
            .map(|_| ())
            .map_err(user_message)
    }

    pub async fn attach_document(input_id: &str) -> Result<DocumentInfo, &'static str> {
        let value = attach_document_js(input_id)
            .await
            .map_err(document_message)?;
        serde_wasm_bindgen::from_value(value).map_err(|_| "PDFの情報を確認できない")
    }

    pub fn stop_session() {
        let _ = stop_session_js();
    }

    fn error_code(error: JsValue) -> Option<String> {
        js_sys::Error::from(error).message().as_string()
    }

    fn user_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
            Some("microphone_unsupported") => {
                "このブラウザでは音声会話を使えない　最新版でためしてみて"
            }
            Some("microphone_permission_denied") => {
                "マイクが許可されていない　ブラウザの権限を確認してみて"
            }
            Some("microphone_unavailable") => {
                "使えるマイクが見つからない　接続を確認してみて"
            }
            Some("no_speech") => "声を拾えなかった　少し近づいてもう一度",
            Some("authentication_failed") => {
                "安全な接続を確認できない　もう一度ためしてみて"
            }
            Some("app_check_not_configured") => {
                "App Check の公開サイトキーがまだない"
            }
            Some("voice_turn_too_large") => "少し長すぎた　短く区切ってみて",
            Some("voice_turn_invalid") => "音声を確認できない　もう一度ためしてみて",
            Some("rate_limited") => "いま少し混み合ってる　少し待って再開してみて",
            Some("request_cancelled") => "会話を一時停止した",
            Some("audio_playback_blocked") => {
                "声を再生できない　端末の消音設定を確認してみて"
            }
            Some("voice_api_unavailable") => {
                "音声エージェントを準備中　少し待ってためしてみて"
            }
            _ => "音声エージェントにつながらない　もう一度ためしてみて",
        }
    }

    fn document_message(error: JsValue) -> &'static str {
        match error_code(error).as_deref() {
            Some("document_not_selected") => "PDFを選んでみて",
            Some("document_type_invalid") => "ここではPDFだけを読める",
            Some("document_too_large") => "PDFは7MBまで",
            Some("document_read_failed") => {
                "PDFを読めなかった　別のファイルをためしてみて"
            }
            _ => "PDFを添付できなかった",
        }
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod cloud {
    use super::{CloudState, DocumentInfo, VoiceTurnResult};

    pub async fn status() -> CloudState {
        CloudState::Unavailable
    }

    pub async fn begin_turn() -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn wait_for_turn_end() -> Result<bool, &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn finish_turn(_session_state: &str) -> Result<VoiceTurnResult, &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn play_response(
        _audio_base64: &str,
        _audio_mime_type: &str,
    ) -> Result<(), &'static str> {
        Err("WebAssembly版で使ってみて")
    }

    pub async fn attach_document(_input_id: &str) -> Result<DocumentInfo, &'static str> {
        Err("WebAssembly版で使ってみて")
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
    mut voice_state: Signal<VoiceState>,
    generation: Signal<u64>,
    session_state: Signal<String>,
    detected_domain: Signal<String>,
    route: Signal<String>,
    needs_paper: Signal<bool>,
    document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
) {
    if announce_permission {
        voice_state.set(VoiceState::RequestingPermission);
    }

    spawn(async move {
        if let Err(message) = cloud::begin_turn().await {
            if *generation.peek() == operation {
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
                    voice_state.set(VoiceState::Error(message));
                }
                return;
            }
        };

        if *generation.peek() == operation && *voice_state.peek() == VoiceState::Listening {
            if has_speech {
                submit_turn(
                    operation,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    needs_paper,
                    document_info,
                    caption,
                );
            } else {
                arm_listening(
                    operation,
                    false,
                    voice_state,
                    generation,
                    session_state,
                    detected_domain,
                    route,
                    needs_paper,
                    document_info,
                    caption,
                );
            }
        }
    });
}

#[allow(clippy::too_many_arguments)]
fn submit_turn(
    operation: u64,
    mut voice_state: Signal<VoiceState>,
    generation: Signal<u64>,
    mut session_state: Signal<String>,
    mut detected_domain: Signal<String>,
    mut route: Signal<String>,
    mut needs_paper: Signal<bool>,
    mut document_info: Signal<Option<DocumentInfo>>,
    mut caption: Signal<Option<String>>,
) {
    if *generation.peek() != operation || *voice_state.peek() != VoiceState::Listening {
        return;
    }

    let state_snapshot = session_state.peek().clone();
    let consumed_document = document_info.peek().is_some();
    voice_state.set(VoiceState::Thinking);

    spawn(async move {
        let result = cloud::finish_turn(&state_snapshot).await;
        if *generation.peek() != operation {
            return;
        }

        let result = match result {
            Ok(result) => result,
            Err(message) => {
                if consumed_document {
                    document_info.set(None);
                }
                voice_state.set(VoiceState::Error(message));
                return;
            }
        };

        session_state.set(result.session_state.clone());
        detected_domain.set(result.detected_domain.clone());
        route.set(result.route.clone());
        needs_paper.set(result.needs_paper);
        caption.set(result.caption.clone());
        if consumed_document {
            document_info.set(None);
        }

        voice_state.set(VoiceState::Speaking);
        if let Err(message) =
            cloud::play_response(&result.audio_base64, &result.audio_mime_type).await
        {
            if *generation.peek() == operation {
                voice_state.set(VoiceState::Error(message));
            }
            return;
        }
        if *generation.peek() != operation {
            return;
        }

        arm_listening(
            operation,
            false,
            voice_state,
            generation,
            session_state,
            detected_domain,
            route,
            needs_paper,
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
    document_info: Signal<Option<DocumentInfo>>,
    caption: Signal<Option<String>>,
) {
    let operation = generation.peek().wrapping_add(1);
    generation.set(operation);
    arm_listening(
        operation,
        true,
        voice_state,
        generation,
        session_state,
        detected_domain,
        route,
        needs_paper,
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

#[component]
fn App() -> Element {
    let mut voice_state = use_signal(|| VoiceState::Ready);
    let mut generation = use_signal(|| 0_u64);
    let mut session_state = use_signal(String::new);
    let mut detected_domain = use_signal(String::new);
    let mut route = use_signal(String::new);
    let mut needs_paper = use_signal(|| false);
    let mut document_info = use_signal(|| None::<DocumentInfo>);
    let mut document_error = use_signal(|| None::<&'static str>);
    let mut caption = use_signal(|| None::<String>);
    let mut captions_visible = use_signal(|| false);
    let cloud_status = use_resource(|| async { cloud::status().await });

    let state_snapshot = *voice_state.read();
    let captions_are_visible = *captions_visible.read();
    let document_snapshot = document_info.read().clone();
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
                            span { class: "route-chip", {route.read().clone()} }
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
                                match *voice_state.peek() {
                                    VoiceState::Ready | VoiceState::Error(_) => {
                                        start_or_resume(
                                            voice_state,
                                            generation,
                                            session_state,
                                            detected_domain,
                                            route,
                                            needs_paper,
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
                            {state_snapshot.label()}
                        }
                        h1 { id: "voice-heading",
                            if state_snapshot == VoiceState::Ready {
                                "ただ話す。"
                                br {}
                                "考えは、あとから整う。"
                            } else {
                                {state_snapshot.label()}
                            }
                        }
                        p { class: "voice-status__hint", {state_snapshot.hint()} }
                    }

                    if *needs_paper.read() && document_snapshot.is_none() {
                        p { class: "paper-request", role: "status",
                            span { "↳" }
                            "論文の中身まで検討するには、下からPDFを今回だけ渡してください。"
                        }
                    }

                    nav { class: "session-controls", aria_label: "会話の操作",
                        if state_snapshot.session_active() {
                            button {
                                class: "control-button",
                                r#type: "button",
                                onclick: move |_| {
                                    if *voice_state.peek() == VoiceState::Paused {
                                        start_or_resume(
                                            voice_state,
                                            generation,
                                            session_state,
                                            detected_domain,
                                            route,
                                            needs_paper,
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
                                if state_snapshot == VoiceState::Paused {
                                    span { aria_hidden: "true", "▶" }
                                    "再開"
                                } else {
                                    span { aria_hidden: "true", "Ⅱ" }
                                    "一時停止"
                                }
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
                                    "字幕が届くと、ここにだけ表示します。既定では非表示です。"
                                }
                            }
                        }
                    }
                }

                aside { class: "utility-dock", aria_label: "資料とプライバシー",
                    section { class: "paper-drop",
                        div { class: "paper-drop__heading",
                            span { class: "utility-index", "01" }
                            div {
                                h2 { "論文を、今回だけ" }
                                p { "PDF / 最大7MB / 送信後に端末メモリから破棄" }
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

                    details { class: "privacy-fold",
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
                                "発話ターンの間だけブラウザのメモリに保持し、応答生成のため暗号化通信でクラウドへ送ります。永続ストレージには書き込みません。"
                            }
                            p {
                                strong { "PDF" }
                                "選んだ場合だけ次の応答へ添付し、送信完了後にブラウザ側の参照を破棄します。"
                            }
                            p {
                                strong { "本人性" }
                                "Firebase Authentication と App Check を毎リクエスト検証します。"
                            }
                            p { class: "privacy-fold__stop",
                                "一時停止・終了を押すと、マイクトラックと再生をすぐ停止します。"
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
