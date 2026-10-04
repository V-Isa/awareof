package prettier

const prettierHelper = `
import process from "node:process";
import path from "node:path";
import { pathToFileURL } from "node:url";

const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const request = JSON.parse(Buffer.concat(chunks).toString("utf8"));
const prettier = await import(pathToFileURL(request.prettierEntry).href);
const kinds = new Map(Object.entries(request.configKinds).map(([name, kind]) => [path.resolve(name), kind]));
const results = [];

for (const [index, file] of request.paths.entries()) {
  try {
    const base = await prettier.getFileInfo(file, {
      resolveConfig: false,
      ignorePath: request.ignorePaths,
      withNodeModules: false,
    });
    if (base.ignored) {
      results.push({ index, ignored: true, parser: "", config: "" });
      continue;
    }
    const selected = await prettier.resolveConfigFile(file);
    let parser = base.inferredParser ?? "";
    let config = "";
    if (selected) {
      const normalized = path.resolve(selected);
      const kind = kinds.get(normalized);
	  config = kind === "sentinel" ? "" : path.relative(request.configRoot, normalized).split(path.sep).join("/");
      if (!kind) {
        results.push({
          index,
          unavailable: {
            code: "prettier/config-unrecognized",
            summary: "Prettier selected a configuration that was not safely classified",
            evidence: config,
          },
        });
        continue;
      }
      if (kind === "executable" || kind === "shareable") {
        results.push({
          index,
          unavailable: {
            code: kind === "executable" ? "prettier/config-executable" : "prettier/config-shareable",
            summary: kind === "executable"
              ? "effective Prettier configuration is executable"
              : "effective Prettier configuration imports a shareable package",
            evidence: config,
            action: "use a passive local configuration or accept UNKNOWN",
          },
        });
        continue;
      }
      const options = await prettier.resolveConfig(file, {
        config: normalized,
        editorconfig: false,
        useCache: false,
      });
      if (options?.plugins && (!Array.isArray(options.plugins) || options.plugins.length !== 0)) {
        results.push({
          index,
          unavailable: {
            code: "prettier/plugins-unsupported",
            summary: "effective Prettier configuration uses plugins",
            evidence: config,
            action: "remove plugins from this context or accept UNKNOWN",
          },
        });
        continue;
      }
      if (typeof options?.parser === "string" && options.parser.length !== 0) parser = options.parser;
    }
    results.push({ index, ignored: false, parser, config });
  } catch (error) {
    results.push({
      index,
      unavailable: {
        code: "prettier/evaluation-unavailable",
        summary: "Prettier could not establish file eligibility",
        evidence: String(error?.message ?? error),
      },
    });
  }
}

process.stdout.write(JSON.stringify({ results }));
`
