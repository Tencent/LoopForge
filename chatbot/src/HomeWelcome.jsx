import React, { useId } from "react";
import HomeLogo from "./HomeLogo";
import "./home-welcome.css";

const subtitle = "把想法，变成下一步。";

function TitleSweep() {
  const identity = useId().replaceAll(":", "");
  return (
    <span className="home-welcome-sweep" aria-hidden="true">
      {[3, 2, 1, 0].map((trail) => {
        const gradient = `home-title-ring-${identity}-${trail}`;
        const light = `home-title-light-${identity}-${trail}`;
        return (
          <span
            key={trail}
            className={`home-welcome-orbit${trail ? " home-welcome-orbit-trail" : ""}`}
            style={{ "--sweep-lag": `${trail * 65}ms` }}
          >
            <svg viewBox="0 0 48 48" fill="none" focusable="false">
              <defs>
                <linearGradient id={gradient} x1="7" y1="5" x2="38" y2="45" gradientUnits="userSpaceOnUse">
                  <stop stopColor="var(--home-logo-cyan)" />
                  <stop offset=".5" stopColor="var(--home-logo-blue)" />
                  <stop offset="1" stopColor="var(--home-logo-violet)" />
                </linearGradient>
                {!trail ? <radialGradient id={light}>
                  <stop stopColor="var(--home-logo-highlight)" stopOpacity=".04" />
                  <stop offset=".56" stopColor="var(--home-logo-cyan)" stopOpacity=".14" />
                  <stop offset="1" stopColor="var(--home-logo-cyan)" stopOpacity=".025" />
                </radialGradient> : null}
              </defs>
              <circle cx="24" cy="24" r="21.5" stroke={`url(#${gradient})`}
                strokeWidth={trail ? ".7" : "1.5"} fill={trail ? "none" : `url(#${light})`} />
              {!trail ? <g className="home-welcome-orbit-satellite">
                <circle cx="40" cy="10" r="2.1" fill="var(--surface)" stroke="var(--home-logo-violet)" strokeWidth="1.2" />
              </g> : null}
            </svg>
          </span>
        );
      })}
    </span>
  );
}

export default function HomeWelcome() {
  return (
    <div className="welcome home-welcome">
      <div className="home-welcome-lockup">
        <HomeLogo />
        <div className="home-welcome-copy">
          <h1 className="home-welcome-title">
            <span className="home-welcome-title-ink">LoopForge</span>
            <span className="home-welcome-title-light" aria-hidden="true">LoopForge</span>
            <TitleSweep />
          </h1>
          <p className="home-welcome-subtitle">
            <span className="sr-only">{subtitle}</span>
            <span className="home-welcome-typewriter" aria-hidden="true">
              <span className="home-welcome-subtitle-text">{subtitle}</span>
              <span className="home-welcome-caret" />
            </span>
          </p>
        </div>
      </div>
    </div>
  );
}
