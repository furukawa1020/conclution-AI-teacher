# Semantica shadow 実行資源計測

この計測は、Cloud Run revision・source commit・Cloud Build ID・immutable image digest・観測区間を同時に固定する。別revisionの値や、認証拒否された403を成功応答へ混ぜない。

使用する公式指標は次の5つである。

- `run.googleapis.com/container/startup_latencies`: 新しいcontainer instanceの起動時間。
- `run.googleapis.com/container/memory/usage`: containerの実メモリ使用量。
- `run.googleapis.com/request_latencies`: 起動時間を含まないrevision内の要求処理時間。response code 204だけを集計する。
- `run.googleapis.com/request_latency/e2e_latencies`: Google network入口から応答送信まで。response code 204だけを集計する。
- `run.googleapis.com/request_latency/pending`: instance割当・起動待ちを含むpending時間。response code 204だけを集計する。

Cloud Run metricは60秒ごとにsampleされ、表示まで最大120秒遅れる。要求処理時間はcontainer startupを含まないため、startup latencyと分離して報告する。定義は[Google Cloud metrics](https://cloud.google.com/monitoring/api/metrics_gcp_p_z#run)と[Cloud Run monitoring](https://cloud.google.com/run/docs/monitoring)に従う。

まず本文を含まない固定graphを、実際のcaller identityから再構成する。runnerは同名Jobが存在すれば上書きせず停止し、自分で作成した一時Jobだけを`finally`で削除する。

```powershell
& .\services\semantica-shadow\run-graph-probe.ps1 `
  -ProjectId kotae-ai-u22-2026 `
  -ImageDigest asia-northeast1-docker.pkg.dev/kotae-ai-u22-2026/cloud-run-source-deploy/semantica-shadow@sha256:6e4693c57b2c26af46ef87ea4dc1863e461241aa8ce38486370c134c5f676ebb `
  -TargetUrl https://kotae-semantica-shadow-r6kgkvtrmq-an.a.run.app `
  -RequestCount 20
```

Cloud Monitoringへの反映を120秒以上待ってからsnapshotを再生成する。

```powershell
& .\services\semantica-shadow\measure.ps1 `
  -ProjectId kotae-ai-u22-2026 `
  -Revision kotae-semantica-shadow-00002-jwx `
  -ImageDigest sha256:6e4693c57b2c26af46ef87ea4dc1863e461241aa8ce38486370c134c5f676ebb `
  -StartTime 2026-09-02T23:10:00Z `
  -EndTime 2026-09-02T23:40:00Z `
  -SourceCommit 6ddd8fe2e50d5e5825a2d055c9eaa9c9f3f222b1 `
  -BuildId efb7c42b-c4ae-4031-ad2b-60a20f89f507 `
  -OutputPath config\semantica-shadow-runtime-measurement.json
```

標本数が少ない時にp50やp95を外挿しない。snapshotはdistributionの非ゼロ標本数、観測数、点平均の加重平均・最小・最大だけを保存する。性能比較を主張するには、独立した複数cold startと十分な成功要求数を追加取得する。
