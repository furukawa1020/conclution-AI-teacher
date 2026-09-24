# MoonBit interrupt scheduler experiment

Issue #303 is an adoption gate, not a production dependency.

The experiment ports only the content-free finite-state interrupt scheduler.
It never receives audio samples or transcript text. State and result are packed
into one i64 so the hot call allocates no array.

## Pinned toolchain

- moon: 0.1.20260920, commit 914d7da
- moonc: 0.10.14+7d59c7ec9
- Windows x86_64 zip SHA-256:
  faae225a8287d0ce69e44b5b3f754af988e97f4446056d8f32ceb3ddb998fce7
- core zip SHA-256:
  63e5b99991ac8fd49556b1e17bdcbdc662d797000250dd11ef090f38a2175e84

The toolchain is installed under the ignored .tools directory. Generated
artifacts stay under ignored _build.

## Reproduction

From the repository root, set MOON_HOME to .tools/moonbit-issue-303 and prepend
its bin directory to PATH. Then run:

    moon -C experiments/moonbit_interrupt_scheduler check --target wasm
    moon -C experiments/moonbit_interrupt_scheduler test --target wasm
    moon -C experiments/moonbit_interrupt_scheduler build --target wasm --release
    cargo build --manifest-path experiments/moonbit_interrupt_scheduler/rust_reference/Cargo.toml --target wasm32-unknown-unknown --release
    node experiments/moonbit_interrupt_scheduler/benchmark.mjs
    npm install --prefix experiments/moonbit_interrupt_scheduler
    npm exec --prefix experiments/moonbit_interrupt_scheduler playwright test browser-benchmark.spec.mjs --workers=1 --reporter=line

The benchmark first compares 100,000 deterministic transitions against Rust
audio_core through an identical packed-i64 raw-Wasm ABI. It aborts on the first
mismatch. This prevents JS allocation differences from being mistaken for a
language/compiler gain. Node timings are screening data only; the adoption
decision requires the paired Chrome benchmark and no increase in the total
production payload.

## 2026-09-25 decision

MoonBit is not adopted in the production bundle.

- Semantic differential: 100,000 / 100,000 transitions matched Rust.
- Chrome 153, 25 paired samples:
  - cold p95: MoonBit 7.90 ms, Rust 2.80 ms
  - one million steps p95: MoonBit 99.80 ms, Rust 118.90 ms
  - artifact: MoonBit 1,998 bytes, Rust 1,234 bytes
- MoonBit improved warm p95 by 16.1%, below the 20% gate, while cold p95 and
  artifact size regressed.

The useful result was applied without adding a runtime: production now uses the
same packed-i64 ABI in Rust/Wasm and removes the old Float64Array result export.
