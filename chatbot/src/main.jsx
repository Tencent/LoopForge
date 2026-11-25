import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import SopDesigner from "./SopDesigner";
import { isDesignerSurface } from "./sop-designer.js";
import "./styles.css";
import "./home.css";
import "./navigation.css";
import "../../web/app/hover-ink.css";
import "../../web/app/hover-ink.js";
import "./hui-tokens.css";
import "../../web/app/hui-controls.css";
import "../../web/app/hui-tooltip.js";
import "./navigation-motion.css";
import "./composer-context.css";
import "../../web/app/sliding-tabs.css";
export class Boundary extends React.Component {
  state = { error: null };
  static getDerivedStateFromError(error) {
    return { error };
  }
  render() {
    return this.state.error ? (
      <div className="fatal-error">
        <h1>工作台暂时无法打开</h1>
        <p>{this.state.error.message}</p>
        <button onClick={() => location.reload()}>重新加载</button>
      </div>
    ) : (
      this.props.children
    );
  }
}
// The workbench mounts into the platform shell and waits for it to prepare the
// shared application state. The SOP designer keeps its standalone surface.
function MainApplication() {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    let active = true;
    const shell = window.loopforgeBootstrap;
    if (!shell) { setReady(true); return () => { active = false; }; }
    shell.start().then((started) => {
      if (!active || !started) return;
      shell.prepareApplication();
      setReady(true);
    }).catch((error) => shell.fail(error));
    return () => { active = false; };
  }, []);
  if (!ready) return null;
  return <Boundary><App /></Boundary>;
}
const root = document.getElementById("chatRoot") || document.getElementById("root");
if (!root) throw new Error("LoopForge chat root is missing");
if (isDesignerSurface(location.search)) {
  createRoot(root).render(<Boundary><SopDesigner /></Boundary>);
} else {
  createRoot(root).render(<MainApplication />);
}
