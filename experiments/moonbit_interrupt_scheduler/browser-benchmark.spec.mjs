import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { test } from "@playwright/test";

const here = path.dirname(fileURLToPath(import.meta.url));
const moonBytes = readFileSync(
  path.join(here, "_build", "wasm", "release", "build", "interrupt_scheduler.wasm"),
).toString("base64");
const rustBytes = readFileSync(
  path.join(
    here,
    "rust_reference",
    "target",
    "wasm32-unknown-unknown",
    "release",
    "kotae_interrupt_scheduler_reference.wasm",
  ),
).toString("base64");

test.use({ channel: "chrome" });
test.setTimeout(120_000);

test("paired Chrome benchmark", async ({ page, browserName }) => {
  await page.goto("data:text/html,<meta charset=utf-8><title>paired-wasm-benchmark</title>");
  const result = await page.evaluate(async ({ moonBase64, rustBase64 }) => {
    const decode = (base64) => {
      const binary = atob(base64);
      return Uint8Array.from(binary, (value) => value.charCodeAt(0));
    };
    const moon = decode(moonBase64);
    const rust = decode(rustBase64);
    const quantile = (values, q) => {
      const sorted = values.toSorted((a, b) => a - b);
      return sorted[Math.ceil((sorted.length - 1) * q)];
    };
    const cold = async (bytes) => {
      const values = [];
      for (let index = 0; index < 25; index += 1) {
        const start = performance.now();
        await WebAssembly.instantiate(bytes, {});
        values.push(performance.now() - start);
      }
      return { p50: quantile(values, 0.5), p95: quantile(values, 0.95), raw: values };
    };
    const [{ instance: moonInstance }, { instance: rustInstance }] = await Promise.all([
      WebAssembly.instantiate(moon, {}),
      WebAssembly.instantiate(rust, {}),
    ]);
    const run = (advance) => {
      const durations = [];
      let finalChecksum = 0n;
      for (let sample = 0; sample < 25; sample += 1) {
        let state = 24n << 3n;
        let checksum = 0n;
        const start = performance.now();
        for (let index = 0; index < 1_000_000; index += 1) {
          const lane = index & 3;
          const high = lane === 0 || lane === 2;
          state = advance(
            state,
            7,
            high ? 0.081 : 0.041,
            high ? 0.22 : 0.11,
            40,
            (lane + 1) * 40,
            1,
          );
          checksum ^= state;
          if (lane === 3) state = 24n << 3n;
        }
        durations.push(performance.now() - start);
        finalChecksum ^= checksum;
      }
      return {
        p50: quantile(durations, 0.5),
        p95: quantile(durations, 0.95),
        raw: durations,
        checksum: finalChecksum.toString(16),
      };
    };
    return {
      userAgent: navigator.userAgent,
      bytes: { moonbit: moon.byteLength, rust: rust.byteLength },
      coldInitMs: {
        moonbit: await cold(moon),
        rust: await cold(rust),
      },
      oneMillionStepsMs: {
        moonbit: run(moonInstance.exports.advance_intentional_interrupt),
        rust: run(rustInstance.exports.advance_intentional_interrupt_packed),
      },
    };
  }, { moonBase64: moonBytes, rustBase64: rustBytes });
  console.log("KOTAE_MOONBIT_CHROME_RESULT=" + JSON.stringify({
    browserName,
    ...result,
  }));
});
