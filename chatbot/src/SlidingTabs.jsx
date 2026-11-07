import React, { useLayoutEffect, useRef } from "react";
import "../../web/app/sliding-tabs.js";

/** Share the Harness indicator and keyboard behavior with the native UI. */
export default function SlidingTabs({
  as: Element = "div",
  className = "",
  activeSelector,
  children,
  ...props
}) {
  const root = useRef(null);
  const controller = useRef(null);
  useLayoutEffect(() => {
    const mounted = window.AgentFlowSlidingTabs.mount(root.current, {
      activeSelector,
    });
    controller.current = mounted;
    return () => {
      mounted.dispose();
      controller.current = null;
    };
  }, [activeSelector]);
  useLayoutEffect(() => {
    controller.current?.refresh();
  });

  return (
    <Element
      {...props}
      ref={root}
      className={`sliding-tabs ${className}`.trim()}
      data-sliding-tabs=""
      role="tablist"
      aria-orientation="horizontal"
    >
      <span
        className="sliding-tabs-indicator"
        data-sliding-indicator=""
        aria-hidden="true"
      />
      {children}
    </Element>
  );
}
