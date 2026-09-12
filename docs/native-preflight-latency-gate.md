# Native予熱100回実測gate

Issue #236の実測値は、`scripts/evaluate-native-preflight-latency.mjs`で再計算する。
入力は会話本文、音声、UID、credentialを含まないraw JSONに限定する。

coldはWebSocket生成からstrong readyまで、warmはactivate送信からstrong readyまでとする。
東京Cloud Runとus-central1 provider、PC、OS、Google Chrome完全版、Cloud Run revision、network条件、UTC計測時刻を固定する。

coldとwarmをそれぞれ100試行し、失敗試行は`fallback: true`かつ`latencyMs: null`として隠さず残す。
集計器はnearest-rank p50/p95、最大値、fallback率、未使用接続時間、推定費用、speech-end後接続待ちをrawから再計算する。
rawと集計はそれぞれcanonical JSONのSHA-256としてmanifestへ収録する。

```powershell
node scripts/evaluate-native-preflight-latency.mjs .release-evidence/native-preflight-raw.json
```

warm p95が1,000msを超える、cold p95が4,000msを超える、fallback率が5%を超える、speech-end後接続待ちが1件でも0msを超える、200試行の推定費用が100,000 micro-USDを超える場合は失敗する。
合成値を本番実測として扱わない。releaseへの必須化は、同一commitの実Chromeで取得した200試行raw artifactが揃ってから行う。
