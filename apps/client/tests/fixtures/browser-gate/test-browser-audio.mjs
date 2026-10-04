// Local subprocess fixture only: no browser, authentication or cloud calls.
const mode = process.env.KOTAE_BROWSER_GATE_FIXTURE;
const args = process.argv.slice(2);
const value = (name) => args[args.indexOf(name) + 1];
if (value("--dist") !== "fixture path with spaces\\") {
  process.stderr.write("browser_audio_argument_invalid\n");
  process.exitCode = 1;
} else {
  const result = {
    status: "passed", provenance: "release",
    sourceCommit: value("--expected-commit"),
    manifestSha256: value("--expected-manifest-sha256"),
    acousticCoverageWireValidated: true, acousticExchangeabilityValidated: true,
    directWasmGenerationIsolation: true, freshGenerationFrames: 3,
    guestAFirstFivePathValidated: true, guestAFirstSprintSloValidated: true,
    guestQuietOnsetValidated: true, intentionalFastLaneValidated: true,
    observationAddingValidated: true, quietSpectralCompensationValidated: true,
    quietSubbandEvidenceValidated: true, sameContextReuseFrames: 2,
    sameContextReuseIsolated: true, sampleRateHz: 48000,
    senderDetachGuardPassed: true, temporalVadClockValidated: true,
    wrappedFrames: 5, zeroOutputCapture: true,
  };
  const failures = {
    known: "browser_audio_devtools_timeout\n",
    fixture: "browser_audio_fixture_failed_confirmed_frames_timeout\r\n",
    unterminated: "browser_audio_chrome_exited_early",
    unknown: "browser_audio_SECRET_VALUE\nC:\\private\\profile\nBearer PRIVATE_TOKEN\n",
    forgedFixture: "browser_audio_fixture_failed_unexpected_initial_PRIVATE_TOKEN\n",
    embedded: "prefix browser_audio_devtools_timeout\n browser_audio_devtools_timeout\n",
    empty: "",
    debug: "PRIVATE_TOKEN\nError: C:\\private\\profile\nbrowser_audio_cdp_closed\ntrace PRIVATE_TOKEN\n",
    longLine: "x".repeat(300_000) + "browser_audio_devtools_timeout\n",
    flood: "PRIVATE_TOKEN\n".repeat(30_000) + "browser_audio_fixture_timeout\n",
    validJsonFailure: "browser_audio_devtools_timeout\n",
  };
  if (Object.hasOwn(failures, mode)) {
    if (mode === "validJsonFailure" || mode === "flood") {
      if (mode === "flood") process.stdout.write(" ".repeat(300_000));
      process.stdout.write(JSON.stringify(result));
    }
    process.stderr.write(failures[mode]);
    process.exitCode = 1;
  } else if (mode === "success" || mode === "successNoise") {
    if (mode === "successNoise") process.stderr.write("PRIVATE_TOKEN\nbrowser_audio_devtools_timeout\n");
    process.stdout.write(JSON.stringify(result));
  } else if (mode === "oversized") {
    process.stdout.write(" ".repeat(70_000) + JSON.stringify(result));
  } else if (mode === "badJson") {
    process.stdout.write("PRIVATE_TOKEN");
  } else if (mode === "noJson") {
    process.stderr.write("PRIVATE_TOKEN\n");
  } else if (mode === "wrongCommit" || mode === "wrongManifest" || mode === "wrongProvenance") {
    if (mode === "wrongCommit") result.sourceCommit = "c".repeat(40);
    if (mode === "wrongManifest") result.manifestSha256 = "c".repeat(64);
    if (mode === "wrongProvenance") result.provenance = "fixture";
    process.stdout.write(JSON.stringify(result));
  } else {
    process.stderr.write("browser_audio_gate_failed\n");
    process.exitCode = 1;
  }
}
