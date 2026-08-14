# 先回りしない沈黙レシート

沈黙レシートは、AIが利用者の答えを先回りせず、本人由来のAが先に確定した一回のturnだけを示す本文なしの有限proofです。話す速さ、回答の正しさ、能力、上達、本人性は評価しません。

## 二つの独立境界

1. ブラウザはAudioContext由来の整数sample clockが同一session generationで単調に進み、利用者の音声取得開始時にAI再生が重なっていないことを確認します。PCM、文字起こし、時刻、経過秒数はproofへ保持しません。
2. serverはHTTP、streaming HTTP、WebSocketのすべてで既存の `question_bound_input_answer_first` を返します。この値は、screen済み質問instanceへ束縛された本人入力の必要slotが先頭にあることだけを示します。

両方が成立し、terminal phaseが `complete/complete` で、割込みもfallbackもなかった時だけ `ai_waited_for_answer` を一度発行します。公開eventは `{ outcome, version }` の二fieldだけです。

## fail-closed

無音、sample clockの重複・逆行・大きな欠損、異なるgeneration、AI再生との重複、割込み、HTTP fallback、非terminal proof、未知fieldではレシートを出しません。次のturn開始、session終了、`pagehide`、例外では候補を消去します。通常の音声応答自体は、レシート用clock異常だけでは中断しません。
