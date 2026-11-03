import React, { useEffect, useId, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import "./model-provider-icon.css";

// Trusted, bundled brand artwork copied unchanged from Grasp. Source and license
// notices are retained beside the SVGs in assets/provider-logos/README.md.
const brandSources = import.meta.glob("./assets/provider-logos/*.svg", {
  eager: true,
  query: "?raw",
  import: "default",
});

const modelBrands = [
  [/^(?:claude|anthropic)(?:$|[-_.\s:\d])/i, "claude"],
  [/^(?:gpt|chatgpt|codex|openai)(?:$|[-_.\s:\d])|^o[1-9]\d?(?:$|[-_.\s:])/i, "openai"],
  [/^(?:gemini|google)(?:$|[-_.\s:\d])/i, "google"],
  [/^deepseek(?:$|[-_.\s:\d])/i, "deepseek"],
  [/^(?:chatglm|glm|zhipu)(?:$|[-_.\s:\d])/i, "zhipu"],
  [/^kimi(?:$|[-_.\s:\d])/i, "kimi"],
  [/^moonshot(?:ai)?(?:$|[-_.\s:\d])/i, "moonshot"],
  [/^(?:qwen|qvq|alibaba)(?:$|[-_.\s:\d])/i, "qwen"],
  [/^(?:hunyuan|tencent)(?:$|[-_.\s:\d])|^hy(?:\d|[-_.\s:])/i, "hunyuan"],
  [/^minimax(?:$|[-_.\s:\d])/i, "minimax"],
  [/^(?:grok|xai)(?:$|[-_.\s:\d])/i, "xai"],
  [/^(?:doubao|seed|seedream|bytedance)(?:$|[-_.\s:\d])/i, "doubao"],
  [/^(?:ernie|wenxin|baidu|qianfan)(?:$|[-_.\s:\d])/i, "qianfan"],
  [/^(?:zai|z\.ai)(?:$|[-_.\s:\d])/i, "zai"],
];
const fallbackBrands = new Set([
  "codebuddy", "cursor", "opencode", "openrouter", "siliconflow", "trae",
  "github", "gitlab",
]);
const colorBrands = new Set([
  "qwen", "doubao", "siliconflow", "minimax", "zhipu", "hunyuan", "qianfan",
]);

/** A visual label only. Never use inferred brands to route model requests. */
export function modelProviderFor(model) {
  const values = typeof model === "string"
    ? [model]
    : [model?.family, model?.id, model?.name];
  for (const value of values) {
    for (const part of String(value || "").trim().split("/").reverse()) {
      const match = modelBrands.find(([pattern]) => pattern.test(part.trim()));
      if (match) return match[1];
    }
  }
  return values.map((value) => String(value || "").trim().toLowerCase())
    .find((value) => fallbackBrands.has(value)) || "";
}

function artwork(name, prefix) {
  const raw = brandSources[`./assets/provider-logos/${name}.svg`];
  if (!raw) return null;
  const opening = raw.match(/<svg\b[^>]*>/i)?.[0];
  const viewBox = opening?.match(/\bviewBox=["']([^"']+)["']/i)?.[1] || "0 0 24 24";
  const fill = opening?.match(/\bfill=["']([^"']+)["']/i)?.[1] || "#000";
  let body = raw.slice(raw.indexOf(opening) + opening.length, raw.lastIndexOf("</svg>"));
  const ids = new Map();
  body.replace(/\bid\s*=\s*["']([^"']+)["']/g, (_, id) => {
    ids.set(id, `${prefix}-${id}`);
    return _;
  });
  // Each inline copy owns its gradients/clips; repeated rows cannot collide.
  body = body.replace(/\bid\s*=\s*(["'])([^"']+)\1/g,
    (_, quote, id) => `id=${quote}${ids.get(id)}${quote}`);
  body = body.replace(/url\(\s*(["']?)#([^\s)'"\]]+)\1\s*\)/g,
    (original, _, id) => ids.has(id) ? `url(#${ids.get(id)})` : original);
  body = body.replace(/\b((?:xlink:)?href)\s*=\s*(["'])#([^"']+)\2/g,
    (original, attribute, quote, id) => ids.has(id)
      ? `${attribute}=${quote}#${ids.get(id)}${quote}` : original);
  const [x, y, width, height] = viewBox.split(/[\s,]+/).map(Number);
  return { body, viewBox, fill, x, y, width, height };
}

function BrandSvg({ name, identity, motionKey, className = "" }) {
  const prefix = `${identity}-${name}`;
  const mark = artwork(name, prefix);
  if (!mark) return null;
  const glyph = `${prefix}-glyph`;
  const mask = `${prefix}-mask`;
  const glint = `${prefix}-glint`;
  return (
    <svg className={`model-provider-svg ${className}`.trim()}
      viewBox={mark.viewBox} fill={mark.fill} aria-hidden="true" focusable="false">
      <defs>
        <g id={glyph} dangerouslySetInnerHTML={{ __html: mark.body }} />
        <mask id={mask} x={mark.x} y={mark.y} width={mark.width} height={mark.height}
          maskUnits="userSpaceOnUse" style={{ maskType: "alpha" }}>
          <use href={`#${glyph}`} />
        </mask>
        <linearGradient id={glint} x1="0" y1="0" x2="1" y2="0">
          <stop offset="0" stopColor="var(--provider-glint)" stopOpacity="0" />
          <stop offset=".5" stopColor="var(--provider-glint)" stopOpacity=".8" />
          <stop offset="1" stopColor="var(--provider-glint)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <use href={`#${glyph}`} />
      <g mask={`url(#${mask})`}>
        <rect key={motionKey} className="model-provider-glint"
          x={mark.x} y={mark.y - mark.height * .3}
          width={mark.width * .42} height={mark.height * 1.6}
          fill={`url(#${glint})`} />
      </g>
    </svg>
  );
}

export default function ModelProviderIcon({ model, size = 20, className = "" }) {
  const host = useRef(null);
  const identity = `model-provider-${useId().replace(/[^\w-]/g, "")}`;
  const [motionKey, setMotionKey] = useState(0);
  const brand = modelProviderFor(model);

  useEffect(() => {
    const element = host.current;
    if (!element || !brand) return;
    const control = element.closest('button, [role="option"], [role="button"], summary, a[href]') || element;
    const play = () => {
      if (matchMedia("(prefers-reduced-motion: reduce)").matches ||
        control.closest('[disabled], [aria-disabled="true"], [aria-busy="true"], [inert]')) return;
      setMotionKey((key) => key + 1);
    };
    const pointer = (event) => { if (event.pointerType !== "touch") play(); };
    const press = (event) => { if (event.button === 0) play(); };
    const focus = (event) => { if (event.target.matches(":focus-visible")) play(); };
    control.addEventListener("pointerenter", pointer);
    control.addEventListener("pointerdown", press);
    control.addEventListener("focusin", focus);
    return () => {
      control.removeEventListener("pointerenter", pointer);
      control.removeEventListener("pointerdown", press);
      control.removeEventListener("focusin", focus);
    };
  }, [brand]);

  return (
    <span ref={host} aria-hidden="true" data-model-provider={brand || "unknown"}
      className={`model-provider-icon ${colorBrands.has(brand) ? "model-provider-icon--color" : brand === "kimi" ? "model-provider-icon--kimi" : "model-provider-icon--mono"} ${className}`.trim()}
      style={{ width: size, height: size }}>
      {brand ? (
        <>
          <BrandSvg name={brand} identity={identity} motionKey={motionKey}
            className={brand === "kimi" ? "model-provider-svg--light" : ""} />
          {brand === "kimi" ? <BrandSvg name="kimi-dark" identity={identity}
            motionKey={motionKey} className="model-provider-svg--dark" /> : null}
        </>
      ) : <HarnessIcon name="brain" size={Math.max(12, size - 4)} />}
    </span>
  );
}
