import { readFile } from "node:fs/promises";
import { evaluateNativePreflightBenchmark } from "./native-preflight-latency-gate.mjs";

if (process.argv.length !== 3) {
  throw new TypeError("usage: node scripts/evaluate-native-preflight-latency.mjs RAW.json");
}
const raw = JSON.parse(await readFile(process.argv[2], "utf8"));
const result = evaluateNativePreflightBenchmark(raw);
process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
if (!result.passed) process.exitCode = 1;
