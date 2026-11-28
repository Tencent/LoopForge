import React from "react";
import { common, createLowlight } from "lowlight";
import cmake from "highlight.js/lib/languages/cmake";
import dart from "highlight.js/lib/languages/dart";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import elixir from "highlight.js/lib/languages/elixir";
import erlang from "highlight.js/lib/languages/erlang";
import groovy from "highlight.js/lib/languages/groovy";
import haskell from "highlight.js/lib/languages/haskell";
import nginx from "highlight.js/lib/languages/nginx";
import powershell from "highlight.js/lib/languages/powershell";
import protobuf from "highlight.js/lib/languages/protobuf";
import scala from "highlight.js/lib/languages/scala";

const lowlight = createLowlight(common);
lowlight.register({
  cmake,
  dart,
  dockerfile,
  elixir,
  erlang,
  groovy,
  haskell,
  nginx,
  powershell,
  protobuf,
  scala,
});

const MAX_HIGHLIGHT_CHARS = 100_000;
const VISUAL_LANGUAGES = new Set(["svg", "mermaid", "html", "slides", "ppt"]);

const LANGUAGE_ALIASES = {
  golang: "go",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  py: "python",
  pyw: "python",
  pyi: "python",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  ksh: "bash",
  fish: "bash",
  console: "bash",
  shellsession: "bash",
  env: "bash",
  yml: "yaml",
  md: "markdown",
  mdx: "markdown",
  html: "xml",
  htm: "xml",
  xhtml: "xml",
  svg: "xml",
  vue: "xml",
  svelte: "xml",
  plist: "xml",
  jsonc: "json",
  json5: "json",
  jsonl: "json",
  toml: "ini",
  cfg: "ini",
  conf: "ini",
  properties: "ini",
  hcl: "ini",
  tf: "ini",
  terraform: "ini",
  "c++": "cpp",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hh: "cpp",
  h: "c",
  "c#": "csharp",
  cs: "csharp",
  rs: "rust",
  rb: "ruby",
  kt: "kotlin",
  kts: "kotlin",
  ps1: "powershell",
  psm1: "powershell",
  ps: "powershell",
  pwsh: "powershell",
  proto: "protobuf",
  docker: "dockerfile",
  make: "makefile",
  mk: "makefile",
  patch: "diff",
  gql: "graphql",
  ex: "elixir",
  exs: "elixir",
  erl: "erlang",
  hs: "haskell",
  gradle: "groovy",
  objc: "objectivec",
  mm: "objectivec",
  text: "plaintext",
  txt: "plaintext",
  plain: "plaintext",
  log: "plaintext",
};

const EXTENSION_LANGUAGES = {
  go: "go",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "jsx",
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  tsx: "tsx",
  py: "python",
  pyw: "python",
  pyi: "python",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  ksh: "bash",
  fish: "bash",
  json: "json",
  jsonc: "json",
  json5: "json",
  jsonl: "json",
  yml: "yaml",
  yaml: "yaml",
  toml: "ini",
  ini: "ini",
  cfg: "ini",
  conf: "ini",
  properties: "ini",
  env: "bash",
  html: "html",
  htm: "html",
  xhtml: "html",
  xml: "xml",
  xsl: "xml",
  xsd: "xml",
  plist: "xml",
  svg: "svg",
  vue: "vue",
  svelte: "svelte",
  css: "css",
  scss: "scss",
  less: "less",
  md: "markdown",
  mdx: "markdown",
  markdown: "markdown",
  sql: "sql",
  rs: "rust",
  java: "java",
  kt: "kotlin",
  kts: "kotlin",
  swift: "swift",
  rb: "ruby",
  rake: "ruby",
  gemspec: "ruby",
  php: "php",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hh: "cpp",
  hxx: "cpp",
  cs: "csharp",
  lua: "lua",
  r: "r",
  pl: "perl",
  pm: "perl",
  diff: "diff",
  patch: "diff",
  graphql: "graphql",
  gql: "graphql",
  proto: "protobuf",
  ps1: "powershell",
  psm1: "powershell",
  psd1: "powershell",
  scala: "scala",
  sc: "scala",
  dart: "dart",
  ex: "elixir",
  exs: "elixir",
  erl: "erlang",
  hrl: "erlang",
  groovy: "groovy",
  gradle: "groovy",
  cmake: "cmake",
  mk: "makefile",
  dockerfile: "dockerfile",
  wasm: "wasm",
  wat: "wasm",
  m: "objectivec",
  mm: "objectivec",
  hs: "haskell",
  tf: "hcl",
  hcl: "hcl",
  txt: "plaintext",
  log: "plaintext",
};

const BASENAME_LANGUAGES = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  gnumakefile: "makefile",
  "cmakelists.txt": "cmake",
  "go.mod": "go",
  "go.sum": "plaintext",
  "cargo.toml": "ini",
  "cargo.lock": "ini",
  "package.json": "json",
  "tsconfig.json": "json",
  ".bashrc": "bash",
  ".zshrc": "bash",
  ".bash_profile": "bash",
  ".profile": "bash",
  ".env": "bash",
  ".gitignore": "plaintext",
  ".gitattributes": "plaintext",
  ".dockerignore": "plaintext",
  ".editorconfig": "ini",
  nginx: "nginx",
};

const EXTENSIONLESS_FILENAMES = new Set([
  "dockerfile",
  "makefile",
  "gnumakefile",
  "jenkinsfile",
  "vagrantfile",
  "procfile",
  "gemfile",
  "rakefile",
  "brewfile",
  "license",
  "readme",
]);

function isKnownLanguageToken(token) {
  const key = String(token || "").trim().toLowerCase();
  if (!key) return false;
  if (VISUAL_LANGUAGES.has(key)) return true;
  if (Object.hasOwn(LANGUAGE_ALIASES, key)) return true;
  return lowlight.registered(key);
}

function looksLikeFilePath(raw, allowBareFilename = false) {
  const value = String(raw || "").trim();
  if (!value || /^https?:\/\//i.test(value)) return false;
  if (value.includes("/") || value.includes("\\")) return true;
  if (/\.[A-Za-z0-9]{1,12}$/.test(value)) return true;
  if (value.startsWith(".") && value.length > 1) return true;
  if (allowBareFilename) {
    return EXTENSIONLESS_FILENAMES.has(value.toLowerCase());
  }
  return false;
}

export function cleanCodeFilePath(rawPath) {
  const trimmed = String(rawPath || "")
    .trim()
    .replace(/^["']|["']$/g, "");
  if (!trimmed) return "";
  const slash = trimmed.replace(/\\/g, "/");
  const patterns = [
    /^.*?(?:\/\.data)?\/(?:conversations\/shared|conversations|assistant|clarify)\/[^/]+\/ws\/(.+)$/,
    /^.*?\/(?:runner\/)?workspaces\/[^/]+(?:\/ws)?\/(.+)$/,
    /^\/workspace\/(.+)$/,
    /^.*?\/\.data\/[^/]+(?:\/[^/]+)*\/ws\/(.+)$/,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(slash);
    if (match?.[1]) return match[1];
  }
  return trimmed;
}

export function inferLanguageFromPath(filePath) {
  const clean = String(filePath || "").trim();
  if (!clean) return "";
  const base = clean.split(/[\\/]/).filter(Boolean).pop() || "";
  const lowerBase = base.toLowerCase();
  if (Object.hasOwn(BASENAME_LANGUAGES, lowerBase)) {
    return BASENAME_LANGUAGES[lowerBase];
  }
  if (lowerBase.startsWith("dockerfile.") || lowerBase.endsWith(".dockerfile")) {
    return "dockerfile";
  }
  if (lowerBase.startsWith(".env.")) {
    return "bash";
  }
  const dot = lowerBase.lastIndexOf(".");
  if (dot > 0 && dot < lowerBase.length - 1) {
    const ext = lowerBase.slice(dot + 1);
    if (Object.hasOwn(EXTENSION_LANGUAGES, ext)) {
      return EXTENSION_LANGUAGES[ext];
    }
  }
  return "";
}

export function splitPathParts(filePath) {
  const value = String(filePath || "");
  const index = Math.max(value.lastIndexOf("/"), value.lastIndexOf("\\"));
  if (index === -1) return { dir: "", file: value };
  return { dir: value.slice(0, index + 1), file: value.slice(index + 1) };
}

export function formatLineRange(startLine, endLine) {
  if (!startLine) return "";
  if (endLine && endLine !== startLine) return `${startLine}-${endLine}`;
  return String(startLine);
}

function parseTrailingLineSuffix(raw) {
  const text = String(raw || "").trim();
  const hashMatch = /^(.+?)#L(\d+)(?:-L?(\d+))?$/i.exec(text);
  if (hashMatch && looksLikeFilePath(hashMatch[1], true)) {
    return {
      rawPath: hashMatch[1].trim(),
      startLine: Number(hashMatch[2]),
      endLine: hashMatch[3] ? Number(hashMatch[3]) : null,
    };
  }
  const colonMatch = /^(.+?):(\d+)(?:[-:](\d+))?$/.exec(text);
  if (
    colonMatch &&
    colonMatch[1].length > 2 &&
    looksLikeFilePath(colonMatch[1], true)
  ) {
    return {
      rawPath: colonMatch[1].trim(),
      startLine: Number(colonMatch[2]),
      endLine: colonMatch[3] ? Number(colonMatch[3]) : null,
    };
  }
  return { rawPath: text, startLine: null, endLine: null };
}

export function parseCodeBlockMeta(rawLang = "", rawMeta = "") {
  const langToken = String(rawLang || "").trim();
  const metaText = String(rawMeta || "").trim();
  const combined = metaText ? `${langToken} ${metaText}`.trim() : langToken;
  if (!combined) {
    return {
      language: "",
      filePath: "",
      rawPath: "",
      startLine: null,
      endLine: null,
      isReference: false,
      visual: false,
    };
  }

  // Case 1: CODE REFERENCES format: startLine:endLine:filepath or startLine:filepath
  const refMatch = /^(\d+):(?:(\d+):)?(.+)$/.exec(combined);
  if (refMatch && looksLikeFilePath(refMatch[3], true)) {
    const rawPath = refMatch[3].trim();
    const filePath = cleanCodeFilePath(rawPath);
    return {
      language: inferLanguageFromPath(filePath),
      filePath,
      rawPath,
      startLine: Number(refMatch[1]),
      endLine: refMatch[2] ? Number(refMatch[2]) : null,
      isReference: true,
      visual: false,
    };
  }

  // Case 2: language + startLine:endLine:filepath in meta (e.g. ```go 968:1005:/path/to/stream.go)
  if (langToken && metaText) {
    const metaRefMatch = /^(\d+):(?:(\d+):)?(.+)$/.exec(metaText);
    if (metaRefMatch && looksLikeFilePath(metaRefMatch[3], true)) {
      const rawPath = metaRefMatch[3].trim();
      const filePath = cleanCodeFilePath(rawPath);
      const explicitLang = langToken.toLowerCase();
      return {
        language: isKnownLanguageToken(explicitLang)
          ? explicitLang
          : inferLanguageFromPath(filePath),
        filePath,
        rawPath,
        startLine: Number(metaRefMatch[1]),
        endLine: metaRefMatch[2] ? Number(metaRefMatch[2]) : null,
        isReference: true,
        visual: false,
      };
    }
  }

  // Case 3: language:filepath (e.g. ```go:internal/httpapi/stream.go)
  if (!/^https?:/i.test(combined)) {
    const colonLang = /^([A-Za-z][A-Za-z0-9+#_-]*):(.+)$/.exec(combined);
    if (colonLang && isKnownLanguageToken(colonLang[1])) {
      const suffix = parseTrailingLineSuffix(colonLang[2]);
      if (looksLikeFilePath(suffix.rawPath, true)) {
        const filePath = cleanCodeFilePath(suffix.rawPath);
        return {
          language: colonLang[1].toLowerCase(),
          filePath,
          rawPath: suffix.rawPath,
          startLine: suffix.startLine,
          endLine: suffix.endLine,
          isReference: true,
          visual: false,
        };
      }
    }
  }

  // Case 4: filepath:startLine-endLine or filepath#LstartLine-LendLine
  if (!/^https?:/i.test(combined)) {
    const suffixed = parseTrailingLineSuffix(combined);
    if (suffixed.startLine && looksLikeFilePath(suffixed.rawPath, true)) {
      const filePath = cleanCodeFilePath(suffixed.rawPath);
      return {
        language: inferLanguageFromPath(filePath),
        filePath,
        rawPath: suffixed.rawPath,
        startLine: suffixed.startLine,
        endLine: suffixed.endLine,
        isReference: true,
        visual: false,
      };
    }
  }

  // Case 5: language + title=/filename= or filepath in meta
  const namedMatch = /(?:^|\s)(?:filename|title)=["']?([^\s"']+)["']?/i.exec(
    combined,
  );
  if (langToken && namedMatch?.[1]) {
    const rawPath = namedMatch[1].trim();
    const filePath = cleanCodeFilePath(rawPath);
    const language = langToken.toLowerCase();
    return {
      language,
      filePath,
      rawPath,
      startLine: null,
      endLine: null,
      isReference: false,
      visual: VISUAL_LANGUAGES.has(language),
    };
  }
  if (
    langToken &&
    metaText &&
    looksLikeFilePath(metaText) &&
    inferLanguageFromPath(metaText)
  ) {
    const filePath = cleanCodeFilePath(metaText);
    const language = langToken.toLowerCase();
    return {
      language,
      filePath,
      rawPath: metaText,
      startLine: null,
      endLine: null,
      isReference: false,
      visual: VISUAL_LANGUAGES.has(language),
    };
  }

  // Case 6: bare filepath as fence info string (e.g. ```internal/httpapi/stream.go)
  if (
    !isKnownLanguageToken(combined) &&
    looksLikeFilePath(combined) &&
    inferLanguageFromPath(combined)
  ) {
    const filePath = cleanCodeFilePath(combined);
    return {
      language: inferLanguageFromPath(filePath),
      filePath,
      rawPath: combined,
      startLine: null,
      endLine: null,
      isReference: true,
      visual: false,
    };
  }

  // Case 7: standard language tag
  const language = langToken.toLowerCase();
  return {
    language,
    filePath: "",
    rawPath: "",
    startLine: null,
    endLine: null,
    isReference: false,
    visual: VISUAL_LANGUAGES.has(language),
  };
}

export function resolveHighlightLanguage(language) {
  const key = String(language || "").trim().toLowerCase();
  if (!key) return "";
  const mapped = LANGUAGE_ALIASES[key] || key;
  if (mapped === "plaintext") return "";
  return lowlight.registered(mapped) ? mapped : "";
}

function renderHastNodes(nodes, keyPrefix = "h") {
  if (!nodes?.length) return null;
  return nodes.map((node, index) => {
    if (node.type === "text") return node.value;
    if (node.type === "element") {
      const className = Array.isArray(node.properties?.className)
        ? node.properties.className.join(" ")
        : node.properties?.className || undefined;
      return React.createElement(
        node.tagName || "span",
        { key: `${keyPrefix}-${index}`, className },
        renderHastNodes(node.children, `${keyPrefix}-${index}`),
      );
    }
    return null;
  });
}

export function highlightCodeNodes(source, language) {
  const text = String(source ?? "");
  if (!text || text.length > MAX_HIGHLIGHT_CHARS) return null;
  const resolved = resolveHighlightLanguage(language);
  if (!resolved) return null;
  try {
    const tree = lowlight.highlight(resolved, text);
    return {
      language: resolved,
      nodes: renderHastNodes(tree.children),
    };
  } catch {
    return null;
  }
}
