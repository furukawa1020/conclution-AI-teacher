# アーキテクチャ

## 作品の核

コタエーAIの独自性は、生成AIへ回答を投げることではありません。発話が始まる前の迷い、結論までの時間、沈黙、言い直しを端末上で計測し、意味評価と重ねて「答え方の癖」を可視化する点です。

```text
マイク
  │ PCM（端末内だけ）
  ▼
Rust audio_core ──→ 時間特徴 ──→ 校正定規UI
  │
  ├─ 生PCMを継続保持しない
  │
  └─ 保存を選んだ録音
       ▼
Rust audio_vault ──→ 暗号文 ──→ Cipher Gateway ──→ 非公開Storage

回答テキスト ──→ Go API ──→ Genkit ──→ Vertex AI
                    │
                    ├─ Firebase ID token
                    ├─ Firebase App Check
                    └─ Firestore（評価値・版情報）
```

## 技術境界

| 層 | 技術 | 責務 |
|---|---|---|
| 体験 | Rust / Dioxus / Wasm | UI、端末状態、アクセシビリティ |
| 音声特徴 | Rust | VAD、開始時刻、沈黙、発話区間 |
| 録音保護 | Rust / AES-256-GCM | チャンク暗号化、AAD、完全性manifest |
| 意味評価 | Go / Genkit | 型付き評価、出力検証、判定経路 |
| 認証境界 | Firebase Auth / App Check | 人と正規アプリの両方を検証 |
| 永続化 | Firestore / Cloud Storage | 評価メタデータと暗号文を分離 |
| モデル | Vertex AI | 意味判定とLive対話 |

ブラウザが要求するマイク、AudioWorklet、Web Crypto、Firebase Web SDKの呼び出しだけは、監査可能な小さなJavaScriptブリッジに隔離します。アプリ本体の状態・評価ロジック・暗号ロジックはJavaScriptへ置きません。

## 評価経路

1. Fast Judgeが通常回答を短時間で構造化判定する。
2. 信頼度が低い、音声認識が不確実、判定境界に近い回答だけPrecision Pathへ送る。
3. 二経路が不一致の場合だけAdjudicatorが最終判定する。
4. モデル名ではなく、logical model ID、rubric、prompt、schemaの各バージョンを結果へ保存する。

これにより、生成AIを一回呼んだ結果を正解扱いせず、費用と再現性を管理します。

## 配信経路

- 静的Wasm UIと短いREST API: Firebase Hosting
- REST評価: Hosting rewriteからCloud Run `kotae-api`
- 長時間音声: `voice.<domain>` のHTTPS Load Balancer＋Cloud ArmorからCloud Run Live Gateway

Firebase Hosting rewriteには60秒の制約があるため、Live WebSocketを通しません。WSS接続前に通常のHTTPSでAuth＋App Checkを検証し、UID・Origin・セッションへ束縛した短命の一回限りticketを発行します。
