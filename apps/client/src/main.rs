use dioxus::prelude::*;

const MAIN_CSS: Asset = asset!("/assets/main.css");

#[derive(Clone, Copy, PartialEq, Eq)]
enum HistoryMode {
    Managed,
    Vault,
}

fn main() {
    dioxus::launch(App);
}

#[component]
fn App() -> Element {
    let mut answer = use_signal(String::new);
    let mut show_feedback = use_signal(|| false);
    let mut history_mode = use_signal(|| HistoryMode::Managed);

    let answer_value = answer.read().clone();
    let answer_is_empty = answer_value.trim().is_empty();
    let character_count = answer_value.chars().count();

    rsx! {
        document::Link { rel: "stylesheet", href: MAIN_CSS }
        document::Meta { name: "theme-color", content: "#f3f0e8" }
        document::Meta {
            name: "description",
            content: "話し始めの十秒を校正する、結論先出しトレーニング。"
        }

        div { class: "app-shell",
            header { class: "masthead",
                a {
                    class: "wordmark",
                    href: "#main",
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
                    span { class: "stamp stamp--pending", "CLOUD 未接続" }
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

            main { id: "main", class: "workbench",
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
                            show_feedback.set(false);
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
                            disabled: answer_is_empty,
                            onclick: move |_| show_feedback.set(true),
                            span { "この答えを校正" }
                            span { aria_hidden: "true", "↗" }
                        }
                    }
                }

                if *show_feedback.read() {
                    section {
                        class: "proof-result",
                        aria_live: "polite",
                        aria_labelledby: "proof-result-heading",
                        div { class: "proof-result__score",
                            span { class: "score-value", "82" }
                            span { class: "score-unit", "/ 100" }
                            span { class: "score-caption", "先出し度" }
                        }
                        div { class: "proof-result__body",
                            p { class: "proof-result__stamp", "LOCAL PREVIEW / 未評価" }
                            h2 { id: "proof-result-heading", "判断を、最初の句点までに。" }
                            p {
                                "現在は体験確認用の固定赤入れです。Firebase接続後は、Go＋Genkitの構造化判定だけをここへ表示します。"
                            }
                            button { class: "retry-action", r#type: "button", "一文目だけ言い直す →" }
                        }
                    }
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
