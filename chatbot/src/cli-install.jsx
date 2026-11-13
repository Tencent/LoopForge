import { useEffect, useRef, useState } from "react";
import { api, write } from "./api.js";
import HarnessIcon from "./HarnessIcon";

// Keep the sentence in sync with assistantCLIMissingError. The engine id in
// the parentheses is what the install request sends; it is not a package name.
const missingCLI =
  /^(.+)（([a-z0-9-]+)）没有安装在当前服务器上。安装后即可在对话里使用。/;

export function cliInstallHint(text) {
  const match = missingCLI.exec(String(text || "").trim());
  if (!match) return null;
  return { label: match[1], engine: match[2] };
}

export function cliInstallNotice(label) {
  return `${label} 已安装，请重新发送。`;
}

function installMemory(engine) {
  return `loopforge.cli-install.${engine}`;
}

function rememberedInstall(engine) {
  try {
    return sessionStorage.getItem(installMemory(engine)) === "installing";
  } catch {
    return false;
  }
}

function rememberInstall(engine, installing) {
  try {
    if (installing) sessionStorage.setItem(installMemory(engine), "installing");
    else sessionStorage.removeItem(installMemory(engine));
  } catch {
    // A private window can reject storage. The server status still restores it.
  }
}

// A finished install replaces the missing-CLI sentence and the generic
// generation failure that the same turn may have kept.
export function cliFailureSettles(text) {
  const value = String(text || "").trim();
  return (
    value === "AI 助手生成失败，请稍后重试" ||
    value === "AI 助手生成失败，具体原因见诊断信息" ||
    /（[a-z0-9-]+）没有安装在当前服务器上/.test(value)
  );
}

// CLIInstallAction installs the CLI named by a conversation failure.
// The server keeps the download after a reload, so the button asks for the
// current status instead of trusting a click that only this page remembers.
export function CLIInstallAction({ text, onResult }) {
  const hint = cliInstallHint(text);
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;
  const pending = useRef(hint ? rememberedInstall(hint.engine) : false);
  const [phase, setPhase] = useState(() =>
    hint && rememberedInstall(hint.engine) ? "installing" : "checking",
  );
  const [failure, setFailure] = useState("");
  useEffect(() => {
    if (!hint) return undefined;
    let cancelled = false;
    let timer;
    let misses = 0;
    const finish = (message) => {
      pending.current = false;
      rememberInstall(hint.engine, false);
      onResultRef.current?.(message);
    };
    const watch = async () => {
      try {
        const status = await api(
          `/api/assistant/cli-install?engine=${encodeURIComponent(hint.engine)}`,
        );
        if (cancelled) return;
        if (status.status === "installed") {
          finish(cliInstallNotice(hint.label));
          return;
        }
        if (status.status === "installing") {
          misses = 0;
          pending.current = true;
          rememberInstall(hint.engine, true);
          setPhase("installing");
          setFailure("");
          timer = setTimeout(watch, 1000);
          return;
        }
        if (status.status === "missing" && pending.current && misses < 2) {
          misses += 1;
          setPhase("installing");
          timer = setTimeout(watch, 1000);
          return;
        }
        pending.current = false;
        rememberInstall(hint.engine, false);
        if (status.status === "failed") setFailure(status.error || "安装失败");
        setPhase("ready");
      } catch {
        if (cancelled) return;
        if (pending.current) {
          timer = setTimeout(watch, 1000);
          return;
        }
        setPhase("ready");
      }
    };
    watch();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [hint?.engine, hint?.label]);
  if (!hint) return null;
  const installing = phase === "installing";
  const label = installing ? "自动安装" : `安装 ${hint.label}`;
  return (
    <span className="credential-action">
      <button
        type="button"
        disabled={phase !== "ready"}
        onClick={() => {
          pending.current = true;
          rememberInstall(hint.engine, true);
          setPhase("installing");
          setFailure("");
          write("/api/assistant/cli-install", { engine: hint.engine })
            .then(() => {
              pending.current = false;
              rememberInstall(hint.engine, false);
              onResultRef.current?.(cliInstallNotice(hint.label));
            })
            .catch((error) => {
              pending.current = false;
              rememberInstall(hint.engine, false);
              setFailure(error.message || "安装失败");
              setPhase("ready");
            });
        }}
      >
        <HarnessIcon name="download" size={13} />
        {label}
      </button>
      <small>安装在服务器上继续，关闭页面也不会中断。</small>
      {failure ? <small>{failure}</small> : null}
    </span>
  );
}
