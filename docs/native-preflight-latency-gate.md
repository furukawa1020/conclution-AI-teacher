# Native予熱100回実測gate

Issue #236の実測値は、公開版を実Chromeで操作して取得する。
会話本文、音声、ユーザーID、credentialは収集しない。
収集対象は、cold/warm所要時間、fallback、speech-end後の接続待ち、未使用接続時間、推定費用だけである。

## 固定する実行環境

実行前に、PC機種、OS版、Chrome版、ネットワーク条件、Cloud Run revision、公開ソースcommitを記録する。
Chromeは普段のprofileと分離した一時profileを使い、remote debuggingをlocalhostだけで起動する。
公開版のタブは1枚だけ開く。

```powershell
& 'C:\Program Files\Google\Chrome\Application\chrome.exe' --remote-debugging-port=9222 --user-data-dir="$env:TEMP\kotae-benchmark-chrome" https://kotae-ai.web.app
```

ゲスト開始後、別terminalで収集器を起動する。
`source-commit`は公開manifest、`runtime-revision`はCloud Run本番revisionと一致させる。
費用係数はproviderの計測日時点の料金から算出し、根拠を実験記録へ併記する。

```powershell
node scripts/collect-native-preflight-latency.mjs `
  --debugging-port 9222 `
  --output native-preflight-raw.json `
  --device-model "実測したPC機種" `
  --operating-system "Windows 11 24H2" `
  --network-condition wifi `
  --runtime-revision kotae-api-example-0912 `
  --source-commit 0123456789abcdef0123456789abcdef01234567 `
  --cost-micro-usd-per-unused-ms 0.001
```

CLIは公開manifestのcommit一致、Google Chrome、公開版タブが厳密に1枚であることを確認する。
その後、content-freeな2種類のブラウザイベントだけをCDP bindingで受け取る。
100試行が完了するまでrawを作らず、途中結果を成功として扱わない。

rawと再計算結果は`.release-evidence/`へ新規ファイルとして原子的に保存し、既存ファイルを上書きしない。
このディレクトリはGit管理外である。
再計算は次のコマンドでも独立に行える。

```powershell
node scripts/evaluate-native-preflight-latency.mjs .release-evidence/native-preflight-raw.json
```

判定はnearest-rank p50/p95、最大値、fallback率、未使用接続時間、推定費用、speech-end後の接続待ちをrawから再計算する。
warm p95が1,000ms超、cold p95が4,000ms超、fallback率が5%超、speech-end後の接続待ちが1件でも0ms超、または100試行の推定費用が100,000 micro-USD超なら失敗する。
同じcommitの実Chrome 100試行rawがない限り、本番実測済みとは主張しない。
