import assert from "node:assert/strict";
import test from "node:test";
import {checkModuleFiles} from "./check-web-module-dependencies.mjs";

test("static imports, reexports and literal dynamic imports must exist",()=>{
 for(const source of ['import "./missing.mjs";', 'export {x} from "./missing.mjs";', 'import("./missing.mjs")']) {
  assert.throws(()=>checkModuleFiles(new Map([["main.js",source]])),/Missing module dependency/);
 }
});
test("cycles and web-root paths are valid without executing modules",()=>{
 assert.equal(checkModuleFiles(new Map([
  ["main.js",'import "./nested/child.mjs"; throw Error("must not execute");'],
  ["nested/child.mjs",'import("/main.js?version=1");'],
 ])),2);
});
test("escaping and unsupported module references are rejected",()=>{
 for(const specifier of ["../outside.js","./%2e%2e/outside.js","node:fs","//external.test/x.js"]) {
  assert.throws(()=>checkModuleFiles(new Map([["main.js",`import "${specifier}";`]])));
 }
});
test("static comment text is not treated as a dependency",()=>{
 assert.equal(checkModuleFiles(new Map([["main.js",'// import "./missing.js";\nexport const x=1;']])),0);
});
