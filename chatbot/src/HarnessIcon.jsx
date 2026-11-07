// SVG definitions and motion adapted from Harness UI (MIT).
// Harness UI upstream commit da2e476895331ef4afee30878af0fb3fb1c73131 (MIT, see HARNESS-UI-LICENSE.txt)
// The static SVG markup below is trusted upstream artwork; see HARNESS-UI-LICENSE.txt.
import React, { useEffect, useRef, useState } from "react";
import definitions from "./harness-icon-definitions.json";
import "./harness-icons.css";

export const HARNESS_ICON_NAMES = Object.freeze(Object.keys(definitions));

const hostSelector =
  '[data-icon-motion-host], button, a[href], summary, [role="button"], [role="tab"], [tabindex]:not([tabindex="-1"])';

/** Harness glyphs respond to hover, keyboard focus and pointer/keyboard activation. */
export default function HarnessIcon({
  name = "sparkles",
  size = 20,
  motion = "idle",
  motionKey,
  interactive = true,
  disabled = false,
  className = "",
  ...svgProps
}) {
  const element = useRef(null);
  const [state, setState] = useState("idle");
  const iconName = Object.hasOwn(definitions, name) ? name : "sparkles";
  const definition = definitions[iconName];
  const ariaHidden =
    svgProps["aria-hidden"] ??
    !(svgProps["aria-label"] || svgProps["aria-labelledby"]);

  useEffect(() => {
    const node = element.current;
    if (!node) return;
    const host = node.closest(hostSelector) || node;
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    let timer, frame;
    let active = true;
    let hovered = false;
    const isDisabled = () =>
      disabled ||
      !!node.closest(
        '[disabled], [aria-disabled="true"], [aria-readonly="true"], [inert]',
      );
    const clear = () => {
      window.clearTimeout(timer);
      window.cancelAnimationFrame(frame);
    };
    const reset = () => {
      clear();
      if (active)
        setState(
          motion === "busy" && !media.matches && !isDisabled()
            ? "busy"
            : "idle",
        );
    };
    const play = () => {
      if (!active || media.matches || isDisabled() || motion === "busy") return;
      clear();
      setState("idle");
      frame = window.requestAnimationFrame(() => {
        if (!active || media.matches || isDisabled()) return;
        // Commit an idle frame so repeated interactions restart the SVG keyframes.
        node.getBoundingClientRect();
        setState("once");
        timer = window.setTimeout(reset, definition.duration + 360);
      });
    };
    const onPointerEnter = (event) => {
      if (event.pointerType === "touch") return;
      hovered = true;
      if (interactive) play();
    };
    const onPointerLeave = () => {
      hovered = false;
    };
    const onPointerDown = (event) => {
      if (interactive && event.button === 0) play();
    };
    const onKeyDown = (event) => {
      if (
        interactive &&
        !event.repeat &&
        (event.key === "Enter" || event.key === " ")
      )
        play();
    };
    const onFocus = (event) => {
      if (
        interactive &&
        !hovered &&
        event.target instanceof Element &&
        event.target.matches(":focus-visible")
      )
        play();
    };

    host.addEventListener("pointerenter", onPointerEnter);
    host.addEventListener("pointerleave", onPointerLeave);
    host.addEventListener("pointerdown", onPointerDown);
    host.addEventListener("keydown", onKeyDown);
    host.addEventListener("focusin", onFocus);
    media.addEventListener("change", reset);
    // A native disabled/inert host can change without changing this icon's props.
    const observer = new MutationObserver(reset);
    for (let ancestor = node; ancestor; ancestor = ancestor.parentElement)
      observer.observe(ancestor, {
        attributes: true,
        attributeFilter: [
          "disabled",
          "aria-disabled",
          "aria-readonly",
          "inert",
        ],
      });
    reset();
    if (motion === "once") play();
    return () => {
      active = false;
      clear();
      observer.disconnect();
      host.removeEventListener("pointerenter", onPointerEnter);
      host.removeEventListener("pointerleave", onPointerLeave);
      host.removeEventListener("pointerdown", onPointerDown);
      host.removeEventListener("keydown", onKeyDown);
      host.removeEventListener("focusin", onFocus);
      media.removeEventListener("change", reset);
    };
  }, [iconName, definition.duration, motion, motionKey, interactive, disabled]);

  return (
    <svg
      {...svgProps}
      ref={element}
      className={`agent-icon harness-icon ${className}`.trim()}
      aria-hidden={ariaHidden}
      role={svgProps.role || (!ariaHidden ? "img" : undefined)}
      focusable="false"
      data-icon={iconName}
      data-icon-motion={state}
      data-icon-disabled={disabled || undefined}
      width={size}
      height={size}
      viewBox={definition.viewBox}
      fill={definition.filled ? "currentColor" : "none"}
      stroke={definition.strokeWidth ? "currentColor" : "none"}
      strokeWidth={svgProps.strokeWidth ?? definition.strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      dangerouslySetInnerHTML={{ __html: definition.body }}
    />
  );
}
