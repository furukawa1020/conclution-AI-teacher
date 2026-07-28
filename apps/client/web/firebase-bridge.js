import { getApp, getApps, initializeApp } from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app.js";
import {
  browserSessionPersistence,
  getAuth,
  getIdToken,
  setPersistence,
  signInAnonymously,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-auth.js";
import {
  getToken as getAppCheckToken,
  initializeAppCheck,
  ReCaptchaEnterpriseProvider,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app-check.js";

const EXPECTED_PROJECT_ID = "kotae-ai-u22-2026";
const EXPECTED_APP_ID = "1:551920539470:web:6518baf6d84d7ab89eb01f";
const RECAPTCHA_SITE_KEY = "__RECAPTCHA_SITE_KEY__";
const API_ENDPOINT = "/api/v1/evaluations";
const QUESTION = "今週金曜までに、試作版を公開できますか。";

const ALLOWED_CONFIG_KEYS = Object.freeze([
  "apiKey",
  "appId",
  "authDomain",
  "databaseURL",
  "measurementId",
  "messagingSenderId",
  "projectId",
  "storageBucket",
]);

let servicesPromise;

function fail(code) {
  throw new Error(code);
}

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function verifiedConfig(raw) {
  if (!isPlainRecord(raw)) {
    fail("firebase_config_invalid");
  }
  if (raw.projectId !== EXPECTED_PROJECT_ID || raw.appId !== EXPECTED_APP_ID) {
    fail("firebase_project_mismatch");
  }
  if (
    typeof raw.apiKey !== "string" ||
    raw.apiKey.length < 20 ||
    typeof raw.authDomain !== "string" ||
    !raw.authDomain.endsWith(".firebaseapp.com")
  ) {
    fail("firebase_config_invalid");
  }

  const config = Object.create(null);
  for (const key of ALLOWED_CONFIG_KEYS) {
    const value = raw[key];
    if (typeof value === "string" && value.length > 0) {
      config[key] = value;
    }
  }
  return Object.freeze(config);
}

async function loadFirebaseConfig() {
  const response = await fetch("/__/firebase/init.json", {
    cache: "no-store",
    credentials: "same-origin",
    redirect: "error",
    referrerPolicy: "no-referrer",
  });
  if (!response.ok) {
    fail("firebase_config_unavailable");
  }
  return verifiedConfig(await response.json());
}

function siteKeyConfigured() {
  return (
    RECAPTCHA_SITE_KEY !== "__RECAPTCHA_SITE_KEY__" &&
    RECAPTCHA_SITE_KEY.length >= 20
  );
}

async function initializeServices() {
  if (!siteKeyConfigured()) {
    fail("app_check_not_configured");
  }

  const config = await loadFirebaseConfig();
  const app = getApps().length === 0 ? initializeApp(config) : getApp();
  if (
    app.options.projectId !== EXPECTED_PROJECT_ID ||
    app.options.appId !== EXPECTED_APP_ID
  ) {
    fail("firebase_project_mismatch");
  }

  const auth = getAuth(app);
  await setPersistence(auth, browserSessionPersistence);
  const credential = auth.currentUser
    ? { user: auth.currentUser }
    : await signInAnonymously(auth);
  const appCheck = initializeAppCheck(app, {
    provider: new ReCaptchaEnterpriseProvider(RECAPTCHA_SITE_KEY),
    isTokenAutoRefreshEnabled: true,
  });

  return Object.freeze({
    appCheck,
    auth,
    user: credential.user,
  });
}

function services() {
  servicesPromise ??= initializeServices();
  return servicesPromise;
}

function safeEvaluation(payload) {
  if (!isPlainRecord(payload) || !isPlainRecord(payload.evaluation)) {
    fail("evaluation_response_invalid");
  }
  const source = payload.evaluation;
  if (
    !Number.isInteger(source.calibrationScore) ||
    source.calibrationScore < 0 ||
    source.calibrationScore > 100 ||
    typeof source.feedback !== "string" ||
    source.feedback.length === 0 ||
    source.feedback.length > 500 ||
    typeof source.retryInstruction !== "string" ||
    source.retryInstruction.length === 0 ||
    source.retryInstruction.length > 500 ||
    typeof source.modelLogicalId !== "string" ||
    source.modelLogicalId.length === 0 ||
    source.modelLogicalId.length > 120
  ) {
    fail("evaluation_response_invalid");
  }

  return Object.freeze({
    score: source.calibrationScore,
    feedback: source.feedback,
    retryInstruction: source.retryInstruction,
    modelLogicalId: source.modelLogicalId,
  });
}

async function getStatus() {
  if (!siteKeyConfigured()) {
    return Object.freeze({
      state: "configuration-required",
      label: "CLOUD 設定待ち",
    });
  }
  try {
    await services();
    return Object.freeze({
      state: "ready",
      label: "CLOUD 接続済み",
    });
  } catch {
    return Object.freeze({
      state: "unavailable",
      label: "CLOUD 接続エラー",
    });
  }
}

async function evaluate(question, answer) {
  if (question !== QUESTION) {
    fail("question_mismatch");
  }
  if (
    typeof answer !== "string" ||
    answer.trim().length === 0 ||
    Array.from(answer).length > 8000
  ) {
    fail("answer_invalid");
  }

  const { appCheck, user } = await services();
  const [idToken, appCheckResult] = await Promise.all([
    getIdToken(user, false),
    getAppCheckToken(appCheck, false),
  ]);

  const response = await fetch(API_ENDPOINT, {
    method: "POST",
    cache: "no-store",
    credentials: "same-origin",
    redirect: "error",
    referrerPolicy: "no-referrer",
    headers: {
      Authorization: `Bearer ${idToken}`,
      "Content-Type": "application/json",
      "X-Firebase-AppCheck": appCheckResult.token,
    },
    body: JSON.stringify({
      question,
      answer,
      mode: "decision",
    }),
  });

  if (!response.ok) {
    if (response.status === 401) fail("authentication_failed");
    if (response.status === 422) fail("answer_not_evaluable");
    if (response.status === 429) fail("rate_limited");
    fail("evaluation_unavailable");
  }
  return safeEvaluation(await response.json());
}

const publicBridge = Object.freeze({
  evaluate,
  getStatus,
});

Object.defineProperty(globalThis, "kotaeCloud", {
  configurable: false,
  enumerable: false,
  value: publicBridge,
  writable: false,
});
