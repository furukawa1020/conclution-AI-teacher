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

## 供給元固定

- upstream: `semantica-agi/semantica`
- version: `0.6.5`
- license: MIT
- PyPI wheel: `semantica-0.6.5-py3-none-any.whl`
- SHA-256: `5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0`
- CycloneDX SBOM: `config/semantica-shadow-sbom.cdx.json`

Semantica 0.6.5 は ML/画像系を含む大きな依存集合を持つため、既存 `kotae-api` image へ同居させない。受信サービスを配備して IAM 呼出元を `kotae-api` service account のみに制限するまで、本番の `KOTAE_SEMANTICA_SHADOW_ENABLED` は `false` のままとする。
