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
  -ImageDigest asia-northeast1-docker.pkg.dev/kotae-ai-u22-2026/cloud-run-source-deploy/semantica-shadow@sha256:09f0570b623b25477df49f08ad0114fcdd98f30354cd898633b2eab3ffb85a26 `
  -TargetUrl https://kotae-semantica-shadow-r6kgkvtrmq-an.a.run.app `
  -RequestCount 20
```

Cloud Monitoringへの反映を120秒以上待ってからsnapshotを再生成する。

```powershell
& .\services\semantica-shadow\measure.ps1 `
  -ProjectId kotae-ai-u22-2026 `
  -Revision kotae-semantica-shadow-00003-n4k `
  -ImageDigest sha256:09f0570b623b25477df49f08ad0114fcdd98f30354cd898633b2eab3ffb85a26 `
  -StartTime 2026-09-03T06:50:00Z `
  -EndTime 2026-09-03T07:12:00Z `
  -SourceCommit d8ccd3e604a660daa8a7051c46716bb7fa663077 `
  -BuildId e9060540-6503-44e2-a869-2250b7c4135e `
  -ExpectedSuccessCount 20 `
  -OutputPath config\semantica-shadow-runtime-measurement.json
```

標本数が少ない時にp50やp95を外挿しない。snapshotはdistributionの非ゼロ標本数、観測数、点平均の加重平均・最小・最大だけを保存する。性能比較を主張するには、独立した複数cold startと十分な成功要求数を追加取得する。

## revision 3 の観測結果

固定graph 20件を `kotae-api-runtime` から送信した結果、20件すべてが204・空本文・`no-store`で完了した。Cloud Run request logでもPOST 204を20件確認した。

| 指標 | 観測数 | 加重平均 | 最大点平均 |
|---|---:|---:|---:|
| container startup | 1 | 42,457.149 ms | 42,457.149 ms |
| container memory usage | 6 | 782,660,949 bytes | 939,323,392 bytes |
| successful request latency | 20 | 3.861 ms | 15.958 ms |
| successful end-to-end latency | 20 | 10.627 ms | 45.283 ms |
| successful pending latency | 20 | 0 ms | 0 ms |

Artifact Registry上のimage sizeは3,351,117,164 bytesだった。起動は重いが、warm後のgraph再構成は短い。したがって、このserviceを音声の同期経路へ同居させず、上限付きnon-blocking shadowとして隔離する設計を維持する。startupは1観測だけなのでp50・p95や一般化したcold-start性能は主張しない。
