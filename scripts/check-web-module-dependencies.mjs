import vm from "node:vm";
import path from "node:path";
import { readdir, readFile, realpath } from "node:fs/promises";
import { fileURLToPath } from "node:url";

// Parse static imports without evaluating application code. Literal dynamic
// imports are conservatively scanned too; arbitrary expressions are not covered.
export function checkModuleFiles(files) {
  let checked = 0;
  for (const [name, source] of files) {
    if (!/\.(?:mjs|js)$/u.test(name)) continue;
    const module = new vm.SourceTextModule(source, { identifier: name });
    const dependencies = [...module.dependencySpecifiers];
    for (const match of source.matchAll(/\bimport\s*\(\s*(["'])([^"'\\]+)\1\s*(?:,|\))/gu)) {
      dependencies.push(match[2]);
    }
    for (const specifier of dependencies) {
      if (specifier.startsWith("https://")) continue;
      if (!specifier.startsWith("./") && !specifier.startsWith("../") && !specifier.startsWith("/")) {
        throw new Error(`Unsupported browser module reference in ${name}`);
      }
      if (specifier.startsWith("//") || /[\\\0]/u.test(specifier)) throw new Error(`Invalid module path in ${name}`);
      const clean = decodeURIComponent(specifier.split(/[?#]/u)[0]);
      const target = path.posix.normalize(specifier.startsWith("/") ? clean.slice(1) : path.posix.join(path.posix.dirname(name), clean));
      if (target === ".." || target.startsWith("../") || /[\\\0]/u.test(target)) throw new Error(`Module escapes distribution in ${name}`);
      if (!files.has(target)) throw new Error(`Missing module dependency: ${name} -> ${target}`);
      checked++;
    }
  }
  return checked;
}

export async function checkDistribution(directory) {
  const root = await realpath(directory);
  const files = new Map();
  async function visit(relative = "") {
    for (const entry of await readdir(path.join(root, relative), { withFileTypes: true })) {
      const name = relative ? `${relative}/${entry.name}` : entry.name;
      if (entry.isSymbolicLink()) throw new Error("Distribution must not contain symbolic links");
      if (entry.isDirectory()) await visit(name);
      else if (entry.isFile()) files.set(name, /\.(?:mjs|js)$/u.test(name) ? await readFile(path.join(root, name), "utf8") : "");
    }
  }
  await visit();
  return checkModuleFiles(files);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 4 || process.argv[2] !== "--dist") throw new Error("Usage: --dist DIRECTORY");
  const checked = await checkDistribution(process.argv[3]);
  console.log(`WEB_LOCAL_MODULE_DEPENDENCIES=${checked}`);
}
