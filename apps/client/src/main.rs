mod microphone;

use dioxus::prelude::*;
use kotae_audio_core::TimingFeatures;
use microphone::{MicrophoneError, MicrophoneSession};
use serde::Deserialize;

const QUESTION: &str = "今週金曜までに、試作版を公開できますか。";

#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
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

fn timing_bucket_class(index: usize, features: TimingFeatures) -> &'static str {
    const BUCKETS: usize = 40;

    if features.elapsed_ms == 0 {
        return "timing-bucket timing-bucket--empty";
    }

    let total = u128::from(features.elapsed_ms);
    let proportional =
        |milliseconds: u64| ((u128::from(milliseconds) * BUCKETS as u128) / total) as usize;
    let initial = proportional(
        features
            .first_voice_ms
            .unwrap_or(features.elapsed_ms)
            .min(features.elapsed_ms),
    )
    .min(BUCKETS);
    let voice = proportional(features.voiced_ms).min(BUCKETS - initial);
    let trailing = proportional(features.trailing_silence_ms).min(BUCKETS - initial - voice);
    let unclassified = BUCKETS - initial - voice - trailing;

    if index < initial {
        "timing-bucket timing-bucket--initial"
    } else if index < initial + voice {
        "timing-bucket timing-bucket--voice"
    } else if index < initial + voice + unclassified {
        "timing-bucket timing-bucket--unclassified"
    } else {
        "timing-bucket timing-bucket--trailing"
    }
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
    use wasm_bindgen::{JsCast, JsValue, closure::Closure};
    use wasm_bindgen_futures::JsFuture;

    let Some(window) = web_sys::window() else {
        return;
    };
    let promise = js_sys::Promise::new(&mut |resolve, _reject| {
        let fallback = resolve.clone();
        let callback = Closure::once_into_js(move || {
            let _ = resolve.call0(&JsValue::UNDEFINED);
        });
        if window
            .set_timeout_with_callback_and_timeout_and_arguments_0(callback.unchecked_ref(), 100)
            .is_err()
        {
            let _ = fallback.call0(&JsValue::UNDEFINED);
        }
    });
    let _ = JsFuture::from(promise).await;
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

#[cfg(target_arch = "wasm32")]
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
    let mut evaluation_generation = use_signal(|| 0_u64);
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
    let remaining_seconds =
        10_u64.saturating_sub(timing_snapshot.elapsed_ms.min(10_000).div_ceil(1_000));
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
                                class: if timing_snapshot.elapsed_ms >= second * 1_000 {
                                    "ruler-tick ruler-tick--elapsed"
                                } else if second < 2 {
                                    "ruler-tick ruler-tick--focus"
                                } else {
                                    "ruler-tick"
                                },
                                span { class: "ruler-tick__number", "{second + 1}" }
                                span { class: "ruler-tick__line" }
                            }
                        }
                    }

                    div {
                        class: if recording_snapshot == RecordingState::Recording {
                            "engine-notice engine-notice--recording"
                        } else {
                            "engine-notice"
                        },
                        role: "status",
                        aria_live: "polite",
                        aria_busy: matches!(
                            recording_snapshot,
                            RecordingState::Starting | RecordingState::Recording
                        ),
                        span {
                            class: "engine-notice__icon",
                            aria_hidden: "true",
                            if recording_snapshot == RecordingState::Recording { "●" } else { "◉" }
                        }
                        div {
                            strong {
                                match recording_snapshot {
                                    RecordingState::Idle => "10秒の端末内測定",
                                    RecordingState::Starting => "マイクの許可を確認しています",
                                    RecordingState::Recording => "録音中 — 端末内だけで解析",
                                    RecordingState::Complete => "測定を終了しました",
                                    RecordingState::Error(_) => "マイク測定を開始できませんでした",
                                }
                            }
                            p {
                                match recording_snapshot {
                                    RecordingState::Idle => {
                                        "音声は保存・送信せず、発話の時間特徴だけをRustで計算します。"
                                            .to_owned()
                                    }
                                    RecordingState::Starting => {
                                        "ブラウザの確認画面で、この測定に使うマイクを選んでください。"
                                            .to_owned()
                                    }
                                    RecordingState::Recording => {
                                        format!(
                                            "残り約{remaining_seconds}秒。いつでも停止できます。"
                                        )
                                    }
                                    RecordingState::Complete => {
                                        "下の実測値を確認できます。元の音声データは残していません。"
                                            .to_owned()
                                    }
                                    RecordingState::Error(message) => message.to_owned(),
                                }
                            }
                        }
                        if matches!(
                            recording_snapshot,
                            RecordingState::Idle
                                | RecordingState::Complete
                                | RecordingState::Error(_)
                        ) {
                            button {
                                class: "text-button text-button--start",
                                r#type: "button",
                                onclick: move |_| {
                                    if matches!(
                                        *recording_state.peek(),
                                        RecordingState::Starting | RecordingState::Recording
                                    ) {
                                        return;
                                    }

                                    stop_microphone(microphone_session, timing_features);
                                    timing_features.set(TimingFeatures::default());
                                    let operation = recording_generation
                                        .peek()
                                        .wrapping_add(1);
                                    recording_generation.set(operation);
                                    recording_state.set(RecordingState::Starting);

                                    spawn(async move {
                                        let started = MicrophoneSession::start().await;
                                        let operation_is_current =
                                            *recording_generation.peek() == operation
                                                && *recording_state.peek()
                                                    == RecordingState::Starting;
                                        if !operation_is_current {
                                            if let Ok(mut stale_session) = started {
                                                stale_session.stop();
                                            }
                                            return;
                                        }

                                        match started {
                                            Ok(session) => {
                                                timing_features.set(session.features());
                                                microphone_session.set(Some(session));
                                                recording_state.set(RecordingState::Recording);

                                                loop {
                                                    wait_for_recording_tick().await;
                                                    if *recording_generation.peek() != operation {
                                                        return;
                                                    }

                                                    let snapshot = microphone_session
                                                        .read()
                                                        .as_ref()
                                                        .map(|session| {
                                                            (
                                                                session.is_active(),
                                                                session.features(),
                                                                session.analysis_error(),
                                                            )
                                                        });
                                                    let Some((active, features, analysis_error)) =
                                                        snapshot
                                                    else {
                                                        return;
                                                    };
                                                    timing_features.set(features);

                                                    if let Some(error) = analysis_error {
                                                        recording_generation
                                                            .set(operation.wrapping_add(1));
                                                        stop_microphone(
                                                            microphone_session,
                                                            timing_features,
                                                        );
                                                        recording_state.set(
                                                            RecordingState::Error(
                                                                microphone_error_message(error),
                                                            ),
                                                        );
                                                        return;
                                                    }

                                                    if !active {
                                                        stop_microphone(
                                                            microphone_session,
                                                            timing_features,
                                                        );
                                                        recording_state
                                                            .set(RecordingState::Complete);
                                                        return;
                                                    }
                                                }
                                            }
                                            Err(error) => {
                                                recording_state.set(RecordingState::Error(
                                                    microphone_error_message(error),
                                                ));
                                            }
                                        }
                                    });
                                },
                                if recording_snapshot == RecordingState::Complete {
                                    "もう一度測る"
                                } else {
                                    "マイクで測る"
                                }
                            }
                        } else {
                            button {
                                class: "text-button text-button--stop",
                                r#type: "button",
                                onclick: move |_| {
                                    let current = *recording_state.peek();
                                    let next_generation =
                                        recording_generation.peek().wrapping_add(1);
                                    recording_generation.set(next_generation);
                                    match current {
                                        RecordingState::Starting => {
                                            recording_state.set(RecordingState::Idle);
                                        }
                                        RecordingState::Recording => {
                                            stop_microphone(
                                                microphone_session,
                                                timing_features,
                                            );
                                            recording_state.set(RecordingState::Complete);
                                        }
                                        _ => {}
                                    }
                                },
                                if recording_snapshot == RecordingState::Starting {
                                    "キャンセル"
                                } else {
                                    "測定を停止"
                                }
                            }
                        }
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
                        disabled: evaluation_running,
                        value: "{answer_value}",
                        placeholder: "例：はい、金曜までに試作版を公開できます。理由は…",
                        oninput: move |event| {
                            answer.set(event.value());
                            let next_generation =
                                evaluation_generation.peek().wrapping_add(1);
                            evaluation_generation.set(next_generation);
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
                                let request_generation =
                                    evaluation_generation.peek().wrapping_add(1);
                                evaluation_generation.set(request_generation);
                                evaluation_state.set(EvaluationState::Loading);
                                spawn(async move {
                                    let result =
                                        cloud::evaluate(QUESTION, &submitted_answer).await;
                                    if *evaluation_generation.peek() != request_generation {
                                        return;
                                    }
                                    let next_state = match result {
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
                            p { class: "eyebrow", "LOCAL TIMING / 実測" }
                            h2 { id: "answer-print-heading", "声を残さず、間を測る" }
                        }
                        span {
                            class: "prototype-badge",
                            if recording_snapshot == RecordingState::Recording {
                                "LIVE"
                            } else if has_timing_measurement {
                                "測定済み"
                            } else {
                                "未測定"
                            }
                        }
                    }
                    p { class: "answer-print__lead",
                        "文字起こしや話者識別は行いません。マイクの波形は短いメモリ領域で解析後に上書きし、時間特徴だけを画面に表示します。"
                    }
                    div {
                        class: if has_timing_measurement {
                            "answer-print__track answer-print__track--measured"
                        } else {
                            "answer-print__track answer-print__track--empty"
                        },
                        role: "img",
                        aria_label: if has_timing_measurement {
                            "端末内で測定した発話タイミング"
                        } else {
                            "まだ測定されていません"
                        },
                        if has_timing_measurement {
                            for bucket in 0..40 {
                                span {
                                    key: "{bucket}",
                                    class: timing_bucket_class(bucket, timing_snapshot),
                                    aria_hidden: "true"
                                }
                            }
                        } else {
                            span { class: "track-empty-label", "マイクで測ると、ここに実測線が現れます" }
                        }
                    }
                    if has_timing_measurement {
                        p { class: "answer-print__ratio-note",
                            "40セルは集計比率です。発話内の順序や内容を復元する表示ではありません。"
                        }
                    }
                    dl { class: "timing-metrics", aria_live: "polite",
                        div {
                            dt { "発話開始" }
                            dd {
                                if has_timing_measurement {
                                    "{format_first_voice(timing_snapshot.first_voice_ms)}"
                                } else {
                                    "—"
                                }
                            }
                        }
                        div {
                            dt { "発話判定" }
                            dd {
                                if has_timing_measurement {
                                    "{format_duration(timing_snapshot.voiced_ms)}"
                                } else {
                                    "—"
                                }
                            }
                        }
                        div {
                            dt { "末尾の無音" }
                            dd {
                                if has_timing_measurement {
                                    "{format_duration(timing_snapshot.trailing_silence_ms)}"
                                } else {
                                    "—"
                                }
                            }
                        }
                        div {
                            dt { "発話区間" }
                            dd {
                                if has_timing_measurement {
                                    "{timing_snapshot.speech_segments} 回"
                                } else {
                                    "—"
                                }
                            }
                        }
                    }
                    p { class: "answer-print__privacy-note",
                        "この測定では音声の保存・再生・送信は行いません。表示値はページを閉じると失われます。"
                    }
                }

                section { class: "privacy-drawer", aria_labelledby: "privacy-heading",
                    div { class: "privacy-drawer__intro",
                        p { class: "eyebrow", "VOICE VAULT / DESIGN" }
                        h2 { id: "privacy-heading", "履歴機能は、まだ設計段階。" }
                        p { "現在の測定は保存しません。次の二方式は、履歴を実装する際の設計候補です。" }
                    }
                    div { class: "mode-switch", aria_label: "検討中の音声履歴方式",
                        article { class: "mode-card mode-card--planned",
                            span { class: "mode-card__check", aria_hidden: "true", "01" }
                            strong { "管理型セキュア履歴" }
                            small { "設計案：監査付きの復号で再評価に対応" }
                            span { class: "mode-card__status", "未実装" }
                        }
                        article { class: "mode-card mode-card--planned",
                            span { class: "mode-card__check", aria_hidden: "true", "02" }
                            strong { "本人解除 Vault" }
                            small { "設計案：本人の解除なしでは復号しない" }
                            span { class: "mode-card__status", "未実装" }
                        }
                    }
                    p { class: "privacy-drawer__truth",
                        "今使えるのは、音声を保存しない10秒の端末内タイミング測定だけです。"
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn timing_buckets_use_measured_proportions_without_inline_styles() {
        let features = TimingFeatures {
            elapsed_ms: 10_000,
            first_voice_ms: Some(1_000),
            voiced_ms: 5_000,
            trailing_silence_ms: 2_000,
            speech_segments: 2,
        };

        assert!(timing_bucket_class(0, features).ends_with("--initial"));
        assert!(timing_bucket_class(4, features).ends_with("--voice"));
        assert!(timing_bucket_class(25, features).ends_with("--unclassified"));
        assert!(timing_bucket_class(39, features).ends_with("--trailing"));
    }

    #[test]
    fn timing_buckets_are_empty_before_measurement() {
        assert!(timing_bucket_class(0, TimingFeatures::default()).ends_with("--empty"));
    }
}
