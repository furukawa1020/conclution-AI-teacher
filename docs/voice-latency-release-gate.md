# 音声1秒分散trace release gate

## 測定対象

「最初の有意味音声」は、非ゼロPCMが出力deviceのtimelineへscheduleされた時点とする。WebSocket接続、最初のbinary frame、無音、接続音、receipt、フィラーだけでは成功に数えない。開始点は同じbrowser monotonic clock上の最後の利用者音声sampleとする。

browserとserverの絶対時刻は比較しない。browser側4区間の和を`speechEndToSpeakerWriteMs`と完全一致させる。server側区間は同一processのmonotonic clockだけで測り、利用できない経路は`null`にする。生時刻、音声、PCM、caption、transcript、質問、回答、UID、token、error文字列をschemaに持たせない。

## 固定SLO

- speech endからspeaker write: p50 600ms以下、p95 1000ms以下
- gestureからlistening: p95 1000ms以下
- `http-buffered`、`http-stream`、`native-live`を各100件以上
- 合計300件未満ではpercentileを作らず`insufficient`
- p99は分布確認のため報告するが、現段階では合否閾値に使わない

端末・回線・route別groupも出力する。ただし各groupが100件未満なら標本数だけを出し、percentileを主張しない。分類は固定enumであり、User-Agent、IP、device ID、network addressを入力しない。

## 再現

```powershell
go run ./cmd/voice-latency-gate `
  -observations <content-free-observations.ndjson> `
  -thresholds config/voice-latency-slo.json
```

reportは入力NDJSONとthreshold JSONのSHA-256を含む。`accept`だけがexit code 0、SLO超過または標本不足はexit code 1、schema・引数・file異常はexit code 2となる。

このPRは評価契約を固定する。runtimeから同じschemaを生成する接続は後続PRで行い、計測送信や集計が失敗しても音声hot pathを待たせない。
