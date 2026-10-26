use dioxus::prelude::*;
use serde::Deserialize;

pub fn action_feedback(code: &str) -> &'static str {
    match code {
        "guest_trial_not_recognized" => "音声を認識できませんでした。答えの判定はしていません。もう一度録音できます。",
        "guest_trial_capture_unavailable" => "録音を続けられませんでした。マイクの接続と許可を確認して、もう一度録音してください。",
        "guest_trial_cancelled" => "操作を中止しました。音声は自動で送り直しません。",
        "guest_trial_expired" => "この試行の期限が切れました。中止して、新しい試行を始めてください。",
        "guest_trial_authentication_failed" | "guest_trial_session_changed" => "ゲスト接続を確認できませんでした。中止して入り直してください。",
        _ => "操作を確認できませんでした。状態を確認するか中止してください。",
    }
}

#[cfg(test)]
mod tests {
    use super::action_feedback;

    #[test]
    fn action_feedback_is_finite_and_distinguishes_recovery() {
        assert!(action_feedback("guest_trial_not_recognized").contains("もう一度録音"));
        assert!(action_feedback("guest_trial_capture_unavailable").contains("マイク"));
        assert!(action_feedback("guest_trial_expired").contains("新しい試行"));
        assert!(action_feedback("guest_trial_cancelled").contains("自動で送り直しません"));
        assert_eq!(action_feedback("guest_trial_session_changed"), action_feedback("guest_trial_authentication_failed"));
        assert_eq!(action_feedback("https://private.example?token=secret"), action_feedback("unknown"));
        assert!(!action_feedback("https://private.example?token=secret").contains("secret"));
    }
}

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TrialView {
    pub phase: String,
    pub question: String,
    pub status: String,
    pub assisted: bool,
}

impl Default for TrialView {
    fn default() -> Self {
        Self {
            phase: "idle".into(),
            question: String::new(),
            status: String::new(),
            assisted: false,
        }
    }
}

#[cfg(target_arch = "wasm32")]
struct ResetListener {
    window: web_sys::Window,
    callback: wasm_bindgen::closure::Closure<dyn FnMut(web_sys::Event)>,
}

#[cfg(target_arch = "wasm32")]
impl Drop for ResetListener {
    fn drop(&mut self) {
        use wasm_bindgen::JsCast;
        let _ = self.window.remove_event_listener_with_callback(
            "kotae:guest-trial-reset",
            self.callback.as_ref().unchecked_ref(),
        );
        let _ = self.window.remove_event_listener_with_callback(
            "kotae:guest-trial-capture-interrupted",
            self.callback.as_ref().unchecked_ref(),
        );
    }
}

#[component]
pub fn GuestTrialPanel() -> Element {
    let availability = use_resource(|| async { crate::cloud::guest_trial_available().await });
    let mut view = use_signal(TrialView::default);
    let mut busy = use_signal(|| false);
    let mut feedback = use_signal(String::new);
    let mut epoch = use_signal(|| 0_u64);
    use_drop(|| crate::cloud::reset_guest_trial());
    #[cfg(target_arch = "wasm32")]
    let _listener = use_hook(move || {
        use wasm_bindgen::JsCast;
        let window = web_sys::window()?;
        let callback = wasm_bindgen::closure::Closure::wrap(Box::new(move |event: web_sys::Event| {
            epoch += 1;
            if event.type_() == "kotae:guest-trial-capture-interrupted" {
                let owned = *epoch.read();
                busy.set(true);
                spawn(async move {
                    let recovered = crate::cloud::guest_trial_action("state").await;
                    if *epoch.read() != owned { return; }
                    view.set(recovered.unwrap_or_default());
                    feedback.set("録音が停止しました。音声は送信していません。もう一度録音できます。".into());
                    busy.set(false);
                });
                return;
            }
            view.set(TrialView::default());
            busy.set(false);
            feedback.set(String::new());
        })
            as Box<dyn FnMut(web_sys::Event)>);
        window
            .add_event_listener_with_callback(
                "kotae:guest-trial-reset",
                callback.as_ref().unchecked_ref(),
            )
            .ok()?;
        if window.add_event_listener_with_callback(
            "kotae:guest-trial-capture-interrupted", callback.as_ref().unchecked_ref(),
        ).is_err() {
            let _ = window.remove_event_listener_with_callback(
                "kotae:guest-trial-reset", callback.as_ref().unchecked_ref(),
            );
            return None;
        }
        Some(std::rc::Rc::new(ResetListener { window, callback }))
    });
    if availability.read().as_ref() != Some(&true) {
        return rsx! {};
    }
    let snapshot = view.read().clone();
    let actions: Vec<(&'static str, &'static str)> = if *busy.read() {
        vec![("reset", "中止して録音を消す")]
    } else {
        match snapshot.phase.as_str() {
            "idle" => vec![("begin", "別の質問を自分で答えてみる")],
            "recording" => vec![("submit", "ここまでを送る"), ("stop", "中止する")],
            "active" if snapshot.assisted => vec![("record", "録音を始める"), ("stop", "中止する")],
            "active" => vec![
                ("record", "録音を始める"),
                ("help", "最初の一言だけ促して"),
                ("stop", "中止する"),
            ],
            "uncertain" => vec![("read", "結果の状態を確認する"), ("reset", "中止する")],
            _ => vec![("reset", "この試行を閉じる")],
        }
    };
    rsx! {
        section { class: "guest-a-first-sprint", aria_label: "別の質問を自分で答える",
            h2 { "今度は、別の質問で。" }
            p { "AIは答えを作りません。自分の一言から答えてみます。任意です。" }
            if !snapshot.question.is_empty() { h3 { "{snapshot.question}" } }
            if snapshot.assisted && snapshot.status == "pending" {
                p { "説明の前に、あなたの答えを一言だけ。内容はあなたが決めます。" }
            }
            p { role: "status", aria_live: "polite",
                if *busy.read() { "確認中です。中止できます。" }
                else if snapshot.phase == "recording" { "録音中です。30秒以内に「ここまでを送る」を押してください。" }
                else if snapshot.status == "answer_first" {
                    if snapshot.assisted { "促しのあと、あなたの答えが一言目にありました。" }
                    else { "この質問では、促しなしであなたの答えが一言目にありました。" }
                }
                else if snapshot.status == "unverified" { "今回は、答えが一言目にあることを確認できませんでした。能力の判定ではありません。" }
                else if snapshot.status == "stopped" { "この試行を中止しました。" }
                else if snapshot.phase == "uncertain" { "通信結果が未確認です。音声は自動で送り直しません。" }
            }
            for (action, label) in actions {
                button { class: "control-button", r#type: "button", key: "{action}",
                    onclick: move |_| {
                        epoch += 1;
                        let owned = *epoch.read();
                        busy.set(true); feedback.set(String::new());
                        spawn(async move {
                            let result = crate::cloud::guest_trial_action(action).await;
                            if *epoch.read() != owned { return; }
                            match result {
                                Ok(next) => view.set(next),
                                Err(message) => {
                                    let recovered = crate::cloud::guest_trial_action("state").await;
                                    if *epoch.read() != owned { return; }
                                    if let Ok(next) = recovered {
                                        view.set(next);
                                    }
                                    feedback.set(message.to_string());
                                }
                            }
                            busy.set(false);
                        });
                    },
                    "{label}"
                }
            }
            if !feedback.read().is_empty() { p { role: "alert", "{feedback}" } }
            p { "音声は認識・確認のために送信し、この試行の台帳には保存しません。今回一回の結果であり、上達や日常会話への効果は未評価です。" }
        }
    }
}
