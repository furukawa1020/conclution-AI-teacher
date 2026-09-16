# 人手holdout集計の再現手順

人手評価の素材本文はローカルに置き、Gitへ追加しない。
集計入力は素材全体のSHA-256、盲検順序seed、fixture ID、有限action、匿名rater slotだけを持つ。

同じannotation artifactから同じreport byte列を再生成する。
reportはannotation artifact自体のSHA-256と、評価器を実行した40文字のGit commitを保持する。
日時、端末名、ローカルpathはreportへ混ぜない。

```console
go run ./cmd/mixed-initiative-holdout --input <content-free-annotations.json> --source-commit <40-character-git-commit> > holdout-report.json
```

`content-free-annotations.json`は16 MiB以下とする。
入力schemaにないfield、末尾の別JSON、固定されていないcommitは失敗させる。
同じ入力byte列と同じ評価器commitで2回実行し、reportのSHA-256が一致することを再現確認とする。
