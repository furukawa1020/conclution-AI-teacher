# Rust/Wasm release toolchainの固定境界

release buildは、`config/release-toolchain.json`でreview済みのRust 1.93.0とwasm-bindgen 0.2.126だけを受理します。Rustはtoolchain名だけでなくrustc/cargoのcommit、Wasm target、公式channel manifestの取得元とSHA-256を照合します。wasm-bindgenは公式GitHub release archiveの取得元とSHA-256、展開後実行ファイルのSHA-256、表示versionをすべて照合します。

Windows x86_64とLinux x86_64以外は未reviewとして失敗します。実行ファイルはabsoluteかつ正規化済みpathだけを受理し、そのファイルからfilesystem rootまでにsymlinkまたはreparse pointが一つでもあれば失敗します。これにより、検証後のpath解決を別byte列へ差し替える境界をreleaseから除外します。

## ローカルrelease

```powershell
rustup toolchain install 1.93.0 --profile minimal --target wasm32-unknown-unknown
powershell -ExecutionPolicy Bypass -File scripts/install-release-wasm-bindgen.ps1
$tool = Join-Path $PWD ".tools/wasm-bindgen-0.2.126/windows-x86_64/wasm-bindgen.exe"
powershell -ExecutionPolicy Bypass -File scripts/test-rust-wasm-toolchain.ps1 `
  -WasmBindgenPath $tool
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1 `
  -CargoPath cargo `
  -WasmBindgenPath $tool `
  -ExpectedGitCommit (git rev-parse HEAD)
```

CIも同じinstaller、policy fixture、`build-web.ps1`を呼び、別実装のversion checkを持ちません。release manifest schema v2には公開可能なversion、commit、platform、archive/executable digest、公式取得元だけを記録します。実行ファイルのlocal absolute path、環境変数、archiveや実行ファイルの内容は記録しません。Hosting preflightはこのtoolchain identityもexact matchで再検証します。

固定値は、公式Rust channel manifestと公式wasm-bindgen GitHub release assetを2026-08-12に取得し、公開checksum/API digestと取得byte列を照合してreviewした値です。
