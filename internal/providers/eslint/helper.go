package eslint

const eslintHelper = `
import process from "node:process";
import { pathToFileURL } from "node:url";

const diagnostic = console.error.bind(console);
const stringify = String;
console.log = diagnostic;
console.info = diagnostic;
console.debug = diagnostic;

const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const request = JSON.parse(Buffer.concat(chunks).toString("utf8"));
const loaded = await import(pathToFileURL(request.eslintEntry).href);
const ESLint = loaded.ESLint ?? loaded.default?.ESLint;
if (typeof ESLint !== "function") throw new Error("ESLint API entry point does not export ESLint");

const eslint = new ESLint({
  cwd: request.cwd,
  overrideConfigFile: request.config,
  cache: false,
});
const results = [];

for (const [index, file] of request.paths.entries()) {
  try {
    const ignored = await eslint.isPathIgnored(file);
    if (ignored) {
      results.push({ index, ignored: true, configured: false });
      continue;
    }
    const config = await eslint.calculateConfigForFile(file);
    results.push({ index, ignored: false, configured: config != null });
  } catch (error) {
    results.push({
      index,
      ignored: false,
      configured: false,
      unavailable: {
        code: "eslint/evaluation-unavailable",
        summary: "ESLint could not establish file eligibility",
        evidence: stringify(error?.message ?? error).slice(0, 2048),
      },
    });
  }
}

process.stdout.write(JSON.stringify({ results }), () => process.exit(0));
`
