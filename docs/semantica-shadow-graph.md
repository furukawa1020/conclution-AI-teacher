# Semantica 意味グラフ shadow 境界

Issue #105 の第一段階では、Semantica を音声の同期経路へ入れない。検証済みの有限な回答証明だけを、応答送信後に非同期で別の非公開 Cloud Run サービスへ渡す。

## 送信できる情報

- 固定 schema `qba.v1`
- サーバー request ID を domain-separated HMAC-SHA256 した turn digest
- `Question → RespondentUtterance → Claim → Verification` の固定 node/edge
- `direct` / `restatement` / `unresolved` / `conflict` の固定 relation
- Semantica 0.6.5 の版と wheel SHA-256

音声、文字起こし、回答本文、UID、Firebase token、passkey、state token、モデルの自由文 reasoning は型に存在せず送れない。

## 速度と障害分離

HTTP・NDJSON・WebSocket は正常な最終応答を書き終えた後、容量64の process-local queue へ non-blocking enqueue する。queue が満杯なら shadow 観測を捨てる。worker の外部通信 timeout は250msで、失敗・timeout・未知enumは音声応答、state、表示を一切変更しない。

呼び出し側は1分ごとに、`accepted / dropped_full / dropped_closed / invalid_graph / exported / export_failed / export_timed_out` の累積数だけを構造化logへ出す。この有限カウンタにはrequest digest、graph、発話、文字起こし、回答、UID、token、error文字列を含めない。429・500・timeout・queue飽和を注入する回帰テストでは、shadowなしの基準音声応答とstatus・bodyが同一であることを検証する。

shadowが受理する状態語彙は本番の回答者コーチ状態機械と同じ有限集合に固定する。特に通常会話の`none`、言い直し待ちの`awaiting_restatement / restate`、安全停止の`blocked / retry`、発話権返却の`release`を含む。テスト専用の別名へ写像せず、実際に配信される全状態をfixtureで検証する。

## 本文なしのoffline比較

現行QBA証明のrelationとshadow graphのrelationは、4種類の有限値だけを入力として比較する。一致は`match`、不一致は方向を含む12種類の固定enumへ変換し、集計結果も総数を含む固定14 fieldだけで表す。未知relationや自由文は集計前に拒否し、件数を変えない。これにより、回答本文や不一致理由の自由文をlog・trace・評価artifactへ残さず、同じ有限traceから同一のcanonical JSONを再現できる。

## 供給元固定

- upstream: `semantica-agi/semantica`
- version: `0.6.5`
- license: MIT
- PyPI wheel: `semantica-0.6.5-py3-none-any.whl`
- SHA-256: `5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0`
- CycloneDX SBOM: `config/semantica-shadow-sbom.cdx.json`

Semantica 0.6.5 は ML/画像系を含む大きな依存集合を持つため、既存 `kotae-api` image へ同居させない。受信サービスを配備して IAM 呼出元を `kotae-api` service account のみに制限するまで、本番の `KOTAE_SEMANTICA_SHADOW_ENABLED` は `false` のままとする。
