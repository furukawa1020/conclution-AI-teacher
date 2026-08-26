# 実音声ASR release gate

Issue #97 の release gate は、合成振幅ではなく許諾済みの実録音を private corpus root から読みます。音声、参照文、認識結果は Git、Cloud Run image、Firebase Hosting、CI log、release manifest に含めません。リポジトリが保持するのは exact schema、集計器、取得元と利用条件を記した license manifest の digest、各 asset の SHA-256、内容を含まない集計値だけです。

固定 bucket は `quiet_speech`、`mutter`、`normal_speech`、`cough`、`hvac`、`playback_leak` の6つです。各 bucket は最低10件を要求します。音声は前処理済みの PCM WAV（16 kHz、mono、16 bit）だけを受理し、symlink、root外path、digest差し替え、未知field、欠落結果、重複IDを拒否します。発話bucketだけが別ファイルの参照文を持ち、非発話bucketへ参照文を付けることも拒否します。

評価は同一 manifest に対する baseline と challenger を同時に読みます。小声recall 85%以上、no-speech false activation 0、quiet CER 35%以下、baseline比 quiet CER 1ポイント以上改善、normal CERの劣化2ポイント以内をすべて満たした時だけ `accept` です。出力は corpus digest、bucket件数、整数ppmの指標、有限error bucket、有限failure codeだけで、本文や仮説を出しません。

JVSは日本語の通常声とwhisperを含みますが、公式条件では音声の再配布を禁じ、商用利用には別契約が必要です。そのため音声をこのrepositoryへ入れず、利用権を確認した private corpus にだけ置きます。ノイズassetも同様に、公式配布元の利用条件を license manifest へ固定します。利用権が確認できないassetや、実録音ではない合成fixtureで release gate を通すことはできません。

実行例:

```powershell
go run ./cmd/asr-release-gate `
  -manifest C:\private\kotae-asr\manifest.json `
  -corpus-root C:\private\kotae-asr `
  -baseline C:\private\kotae-asr\baseline.jsonl `
  -challenger C:\private\kotae-asr\challenger.jsonl
```

この評価を個人の能力評価、本人識別、声紋、感情・疾患・属性推定へ流用してはいけません。
