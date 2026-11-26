import React, { useEffect } from "react";
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
function MainApplication({ onMounted }) {
  useEffect(() => { onMounted?.(); }, [onMounted]);
  return <Boundary><App /></Boundary>;
}
function mountWorkbench(root, onMounted) {
  createRoot(root).render(<MainApplication onMounted={onMounted} />);
}
if (window.loopforgeBootstrap) {
  window.loopforgeBootstrap.registerWorkbench(mountWorkbench);
} else {
  const root = document.getElementById("chatRoot");
  if (!root) throw new Error("LoopForge chat root is missing");
  if (isDesignerSurface(location.search)) createRoot(root).render(<Boundary><SopDesigner /></Boundary>);
  else mountWorkbench(root);
}
