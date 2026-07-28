mod microphone;

use dioxus::prelude::*;
use kotae_audio_core::TimingFeatures;
use microphone::{MicrophoneError, MicrophoneSession};
use serde::Deserialize;

const QUESTION: &str = "今週金曜までに、試作版を公開できますか。";

#[derive(Clone, Copy, PartialEq, Eq)]
enum CloudState {
    Connecting,
    Ready,
    Verified,
    ConfigurationRequired,
    Unavailable,
}

impl CloudState {
    fn label(self) -> &'static str {
        match self {
            Self::Connecting => "CLOUD 接続確認中",
            Self::Ready => "CLOUD 準備済み",
            Self::Verified => "CLOUD 検証済み",
            Self::ConfigurationRequired => "CLOUD 設定待ち",
            Self::Unavailable => "CLOUD 要確認",
        }
    }

    fn class_name(self) -> &'static str {
        match self {
            Self::Ready | Self::Verified => "stamp stamp--cloud-ready",
            Self::Connecting | Self::ConfigurationRequired => "stamp stamp--pending",
            Self::Unavailable => "stamp stamp--cloud-error",
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum RecordingState {
    Idle,
    Starting,
    Recording,
    Complete,
    Error(&'static str),
}

fn microphone_error_message(error: MicrophoneError) -> &'static str {
    match error {
        MicrophoneError::Unsupported => "このブラウザではマイク測定を利用できません。",
        MicrophoneError::PermissionDenied => {
            "マイクの使用が許可されませんでした。ブラウザの権限設定を確認してください。"
        }
        MicrophoneError::DeviceUnavailable => {
            "利用できるマイクを確認できませんでした。接続状態を確認してください。"
        }
        MicrophoneError::CaptureFailed => {
            "マイクを開始できませんでした。ページを再読み込みしてお試しください。"
        }
        MicrophoneError::AudioGraphFailed | MicrophoneError::DetectorFailed => {
            "端末内の音声解析を開始できませんでした。ページを再読み込みしてお試しください。"
        }
    }
}

fn format_duration(milliseconds: u64) -> String {
    format!("{:.2}秒", milliseconds as f64 / 1_000.0)
}

fn format_first_voice(milliseconds: Option<u64>) -> String {
    milliseconds
        .map(format_duration)
        .unwrap_or_else(|| "未検出".to_owned())
}

fn stop_microphone(
    mut session: Signal<Option<MicrophoneSession>>,
    mut timing: Signal<TimingFeatures>,
) {
    let final_features = session
        .write()
        .take()
        .map(|mut active_session| active_session.stop());
    if let Some(final_features) = final_features {
        timing.set(final_features);
    }
}

#[cfg(target_arch = "wasm32")]
async fn wait_for_recording_tick() {
    gloo_timers::future::TimeoutFuture::new(100).await;
}

#[cfg(not(target_arch = "wasm32"))]
async fn wait_for_recording_tick() {
    std::future::pending::<()>().await;
}

#[derive(Clone, PartialEq)]
enum EvaluationState {
    Idle,
    Loading,
    Success(EvaluationResult),
    Error(&'static str),
}

#[derive(Clone, PartialEq, Deserialize)]
#[serde(rename_all = "camelCase")]
struct EvaluationResult {
    score: u8,
    feedback: String,
    retry_instruction: String,
    model_logical_id: String,
}

#[derive(Deserialize)]
struct BridgeStatus {
    state: String,
}

#[cfg(target_arch = "wasm32")]
mod cloud {
    use super::{BridgeStatus, CloudState, EvaluationResult};
    use wasm_bindgen::prelude::*;

    #[wasm_bindgen]
    extern "C" {
        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = getStatus)]
        async fn get_status_js() -> Result<JsValue, JsValue>;

        #[wasm_bindgen(catch, js_namespace = kotaeCloud, js_name = evaluate)]
        async fn evaluate_js(question: &str, answer: &str) -> Result<JsValue, JsValue>;
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

    pub async fn evaluate(question: &str, answer: &str) -> Result<EvaluationResult, &'static str> {
        let value = evaluate_js(question, answer).await.map_err(|error| {
            let message = js_sys::Error::from(error).message();
            user_message(message.as_string().as_deref())
        })?;
        serde_wasm_bindgen::from_value(value).map_err(|_| "評価結果の形式を確認できませんでした。")
    }

    fn user_message(code: Option<&str>) -> &'static str {
        match code {
            Some("app_check_not_configured") => {
                "App Check の公開サイトキーがまだ設定されていません。"
            }
            Some("authentication_failed") => {
                "安全な接続を確認できませんでした。再度お試しください。"
            }
            Some("answer_not_evaluable") => "回答を評価できませんでした。内容を確認してください。",
            Some("rate_limited") => "短時間の利用上限に達しました。少し待って再度お試しください。",
            Some("firebase_project_mismatch") => "接続先のFirebaseプロジェクトが一致しません。",
            _ => "クラウド評価に接続できませんでした。時間をおいて再度お試しください。",
        }
    }
}

#[cfg(not(target_arch = "wasm32"))]
mod cloud {
    use super::{CloudState, EvaluationResult};

    pub async fn status() -> CloudState {
        CloudState::Unavailable
    }

    pub async fn evaluate(
        _question: &str,
        _answer: &str,
    ) -> Result<EvaluationResult, &'static str> {
        Err("WebAssembly版で利用してください。")
    }
}

fn main() {
    dioxus::launch(App);
}

#[component]
fn App() -> Element {
    let mut answer = use_signal(String::new);
    let mut evaluation_state = use_signal(|| EvaluationState::Idle);
    let mut cloud_verification = use_signal(|| None::<bool>);
    let mut recording_state = use_signal(|| RecordingState::Idle);
    let mut recording_generation = use_signal(|| 0_u64);
    let mut microphone_session = use_signal(|| None::<MicrophoneSession>);
    let mut timing_features = use_signal(TimingFeatures::default);
    let cloud_status = use_resource(|| async { cloud::status().await });

    let answer_value = answer.read().clone();
    let answer_is_empty = answer_value.trim().is_empty();
    let character_count = answer_value.chars().count();
    let evaluation_snapshot = evaluation_state.read().clone();
    let evaluation_running = evaluation_snapshot == EvaluationState::Loading;
    let recording_snapshot = *recording_state.read();
    let timing_snapshot = *timing_features.read();
    let has_timing_measurement =
        timing_snapshot.elapsed_ms > 0 || recording_snapshot == RecordingState::Complete;
    let remaining_seconds = 10_u64
        .saturating_sub(timing_snapshot.elapsed_ms.min(10_000).div_ceil(1_000));
    let initial_silence_ms = timing_snapshot
        .first_voice_ms
        .unwrap_or(timing_snapshot.elapsed_ms);
    let unclassified_ms = timing_snapshot
        .elapsed_ms
        .saturating_sub(initial_silence_ms)
        .saturating_sub(timing_snapshot.voiced_ms)
        .saturating_sub(timing_snapshot.trailing_silence_ms);
    let initial_style = format!("flex: {}", initial_silence_ms.max(1));
    let voice_style = format!("flex: {}", timing_snapshot.voiced_ms.max(1));
    let unclassified_style = format!("flex: {}", unclassified_ms.max(1));
    let trailing_style = format!("flex: {}", timing_snapshot.trailing_silence_ms.max(1));
    let prepared_cloud_state = cloud_status
        .read()
        .as_ref()
        .copied()
        .unwrap_or(CloudState::Connecting);
    let cloud_state = match *cloud_verification.read() {
        Some(true) => CloudState::Verified,
        Some(false) => CloudState::Unavailable,
        None => prepared_cloud_state,
    };

    rsx! {
        div { class: "app-shell",
            header { class: "masthead",
                a {
                    class: "wordmark",
                    href: "#workspace",
                    aria_label: "コタエーAI ホーム",
                    span { class: "wordmark__latin", "KOTAE" }
                    span { class: "wordmark__ja", "コタエーAI" }
                }
                p { class: "masthead__manifesto", "答えを、先に。" }
                div { class: "system-stamps", aria_label: "システム状態",
                    span { class: "stamp stamp--local",
                        span { class: "stamp__dot" }
                        "LOCAL / RUST"
                    }
                    span { class: cloud_state.class_name(),
                        if matches!(cloud_state, CloudState::Ready | CloudState::Verified) {
                            span { class: "stamp__dot" }
                        }
                        {cloud_state.label()}
                    }
                }
            }

            aside { class: "index-rail", aria_label: "練習の進行",
                ol { class: "index-rail__list",
                    li { class: "index-rail__item index-rail__item--active",
                        span { class: "index-rail__number", "01" }
                        span { "問い" }
                    }
                    li { class: "index-rail__item",
                        span { class: "index-rail__number", "02" }
                        span { "発話" }
                    }
                    li { class: "index-rail__item",
                        span { class: "index-rail__number", "03" }
                        span { "赤入れ" }
                    }
                    li { class: "index-rail__item",
                        span { class: "index-rail__number", "04" }
                        span { "言い直す" }
                    }
                }
                p { class: "index-rail__note",
                    "音声ではなく、"
                    br {}
                    "答え方の構造を測る。"
                }
            }

            main { id: "workspace", class: "workbench",
                section { class: "question-sheet", aria_labelledby: "question-heading",
                    div { class: "sheet-kicker",
                        span { "TODAY / DECISION" }
                        span { "制限 10秒" }
                    }
                    h1 { id: "question-heading",
                        "今週金曜までに、"
                        br {}
                        "試作版を公開できますか。"
                    }
                    p { class: "question-sheet__instruction",
                        span { class: "proof-mark", "※" }
                        "一文目で「判断」を置く。理由は、そのあと。"
                    }
                }

                section { class: "ruler-panel", aria_labelledby: "ruler-heading",
                    div { class: "section-heading",
                        div {
                            p { class: "eyebrow", "ANSWER RULER / 00—10 SEC" }
                            h2 { id: "ruler-heading", "結論までの距離" }
                        }
                        span { class: "local-only-label", "端末内で解析" }
                    }

                    div { class: "proof-ruler", aria_label: "十秒の校正定規",
                        for second in 0..10 {
                            div {
                                key: "{second}",
                                class: if second < 2 { "ruler-tick ruler-tick--focus" } else { "ruler-tick" },
                                span { class: "ruler-tick__number", "{second + 1}" }
                                span { class: "ruler-tick__line" }
                            }
                        }
                    }

                    div { class: "engine-notice", role: "status",
                        span { class: "engine-notice__icon", aria_hidden: "true", "◉" }
                        div {
                            strong { "ローカル音声エンジンは接続準備中" }
                            p { "現在は画面設計版です。マイクを使ったふりはせず、接続後に録音状態を常時表示します。" }
                        }
                        button { class: "text-button", disabled: true, "マイクを接続" }
                    }
                }

                section { class: "answer-panel", aria_labelledby: "answer-heading",
                    div { class: "section-heading section-heading--answer",
                        div {
                            p { class: "eyebrow", "DRAFT / KEYBOARD PREVIEW" }
                            h2 { id: "answer-heading", "まず文字で答える" }
                        }
                        span { class: "character-count", "{character_count} 字" }
                    }
                    label { class: "sr-only", r#for: "answer-input", "回答" }
                    textarea {
                        id: "answer-input",
                        class: "answer-input",
                        rows: 4,
                        maxlength: 8000,
                        value: "{answer_value}",
                        placeholder: "例：はい、金曜までに試作版を公開できます。理由は…",
                        oninput: move |event| {
                            answer.set(event.value());
                            evaluation_state.set(EvaluationState::Idle);
                        }
                    }
                    div { class: "answer-actions",
                        p {
                            span { class: "shortcut", "⌘ ↵" }
                            " で校正"
                        }
                        button {
                            class: "primary-action",
                            r#type: "button",
                            disabled: answer_is_empty || evaluation_running,
                            onclick: move |_| {
                                let submitted_answer = answer.read().clone();
                                evaluation_state.set(EvaluationState::Loading);
                                spawn(async move {
                                    let next_state = match cloud::evaluate(QUESTION, &submitted_answer).await {
                                        Ok(result) => {
                                            cloud_verification.set(Some(true));
                                            EvaluationState::Success(result)
                                        }
                                        Err(message) => {
                                            cloud_verification.set(Some(false));
                                            EvaluationState::Error(message)
                                        }
                                    };
                                    evaluation_state.set(next_state);
                                });
                            },
                            span {
                                if evaluation_running {
                                    "校正しています…"
                                } else {
                                    "この答えを校正"
                                }
                            }
                            span { aria_hidden: "true", "↗" }
                        }
                    }
                }

                match evaluation_snapshot {
                    EvaluationState::Idle => rsx! {},
                    EvaluationState::Loading => rsx! {
                        section {
                            class: "proof-result proof-result--loading",
                            aria_live: "polite",
                            aria_busy: "true",
                            div { class: "proof-result__score",
                                span { class: "loading-mark", aria_hidden: "true", "◌" }
                                span { class: "score-unit", "評価中" }
                            }
                            div { class: "proof-result__body",
                                p { class: "proof-result__stamp", "CLOUD / GENKIT" }
                                h2 { "答えの構造を校正しています。" }
                                p { "認証・App Checkを確認し、必要な本文だけを評価APIへ送信しています。" }
                            }
                        }
                    },
                    EvaluationState::Error(message) => rsx! {
                        section {
                            class: "proof-result proof-result--error",
                            aria_live: "assertive",
                            div { class: "proof-result__score",
                                span { class: "score-value", "!" }
                                span { class: "score-unit", "未完了" }
                            }
                            div { class: "proof-result__body",
                                p { class: "proof-result__stamp", "CONNECTION ERROR" }
                                h2 { "校正を完了できませんでした。" }
                                p { "{message}" }
                                button {
                                    class: "retry-action",
                                    r#type: "button",
                                    onclick: move |_| evaluation_state.set(EvaluationState::Idle),
                                    "回答を確認して再試行 →"
                                }
                            }
                        }
                    },
                    EvaluationState::Success(result) => rsx! {
                        section {
                            class: "proof-result proof-result--success",
                            aria_live: "polite",
                            aria_labelledby: "proof-result-heading",
                            div { class: "proof-result__score",
                                span { class: "score-value", "{result.score}" }
                                span { class: "score-unit", "/ 100" }
                                span { class: "score-caption", "校正スコア" }
                            }
                            div { class: "proof-result__body",
                                p { class: "proof-result__stamp", "CLOUD EVALUATION" }
                                h2 { id: "proof-result-heading", "{result.feedback}" }
                                p { class: "retry-instruction",
                                    strong { "次の一手：" }
                                    " {result.retry_instruction}"
                                }
                                p { class: "model-label", "MODEL / {result.model_logical_id}" }
                                button {
                                    class: "retry-action",
                                    r#type: "button",
                                    onclick: move |_| evaluation_state.set(EvaluationState::Idle),
                                    "{result.retry_instruction} →"
                                }
                            }
                        }
                    },
                }

                section { class: "answer-print", aria_labelledby: "answer-print-heading",
                    div { class: "section-heading",
                        div {
                            p { class: "eyebrow", "ANSWER PRINT / 答頭線" }
                            h2 { id: "answer-print-heading", "答え方だけを残す" }
                        }
                        span { class: "prototype-badge", "独自プロトタイプ" }
                    }
                    p { class: "answer-print__lead",
                        "声紋でも文字起こしでもない。無音・前置き・結論・根拠の時間構造だけを、過去の自分と比べます。"
                    }
                    div {
                        class: "answer-print__track",
                        role: "img",
                        aria_label: "無音0.8秒、前置き1.4秒、結論1.2秒、根拠4.6秒の例",
                        span { class: "track-segment track-segment--silence", style: "flex: 0.8" }
                        span { class: "track-segment track-segment--preamble", style: "flex: 1.4" }
                        span { class: "track-segment track-segment--conclusion", style: "flex: 1.2",
                            span { "結論" }
                        }
                        span { class: "track-segment track-segment--evidence", style: "flex: 4.6" }
                    }
                    ul { class: "track-legend", aria_label: "答頭線の凡例",
                        li { span { class: "legend-chip legend-chip--silence" } "無音" }
                        li { span { class: "legend-chip legend-chip--preamble" } "前置き" }
                        li { span { class: "legend-chip legend-chip--conclusion" } "結論" }
                        li { span { class: "legend-chip legend-chip--evidence" } "根拠" }
                    }
                }

                section { class: "privacy-drawer", aria_labelledby: "privacy-heading",
                    div { class: "privacy-drawer__intro",
                        p { class: "eyebrow", "VOICE VAULT" }
                        h2 { id: "privacy-heading", "保存方法は、機能ごと選ぶ。" }
                        p { "「全部一時保存」にはしません。履歴と再評価を残しながら、復号できる条件を狭くします。" }
                    }
                    div { class: "mode-switch", role: "radiogroup", aria_label: "音声履歴モード",
                        button {
                            class: if *history_mode.read() == HistoryMode::Managed { "mode-card mode-card--selected" } else { "mode-card" },
                            role: "radio",
                            aria_checked: if *history_mode.read() == HistoryMode::Managed { "true" } else { "false" },
                            onclick: move |_| history_mode.set(HistoryMode::Managed),
                            span { class: "mode-card__check", aria_hidden: "true", "●" }
                            strong { "管理型セキュア履歴" }
                            small { "履歴・再生・自動再評価が使える" }
                        }
                        button {
                            class: if *history_mode.read() == HistoryMode::Vault { "mode-card mode-card--selected" } else { "mode-card" },
                            role: "radio",
                            aria_checked: if *history_mode.read() == HistoryMode::Vault { "true" } else { "false" },
                            onclick: move |_| history_mode.set(HistoryMode::Vault),
                            span { class: "mode-card__check", aria_hidden: "true", "●" }
                            strong { "本人解除 Vault" }
                            small { "本人が開くまでサーバーも復号不可" }
                        }
                    }
                    p { class: "privacy-drawer__truth",
                        if *history_mode.read() == HistoryMode::Managed {
                            "一回限りの認可で再評価できます。復号アクセスは履歴に残ります。"
                        } else {
                            "無人の後日再評価はできません。再評価する時は本人の鍵解除が必要です。"
                        }
                    }
                }
            }

            footer { class: "footer-line",
                span { "KOTAE AI / PROTOTYPE 01" }
                span { "RUST · WASM · GO · FIREBASE · VERTEX AI" }
                span { "RAW VOICE ≠ IDENTITY" }
            }
        }
    }
}
