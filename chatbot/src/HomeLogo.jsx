import React, { useId } from "react";
import "./home-logo.css";

// Four circle segments stay aligned while four folded segments unfold the hook.
const logoPath =
  "M53 72 C53 59 63 52.5 73 52.5 C83 52.5 93 59 93 72 C93 85 83 91.5 73 91.5 C63 91.5 53 85 53 72 C53 66.667 53 61.333 53 56 C53 50.667 53 45.333 53 40 C53 27 44.5 20.5 36 20.5 C27.5 20.5 19 27 19 40";
const ringPath =
  "M24 56 C24 38.327 38.327 24 56 24 C73.673 24 88 38.327 88 56 C88 73.673 73.673 88 56 88 C38.327 88 24 73.673 24 56 C24 56 24 56 24 56 C24 56 24 56 24 56 C24 56 24 56 24 56 C24 56 24 56 24 56";

export default function HomeLogo() {
  const identity = useId().replaceAll(":", "");
  const gradient = `home-logo-gradient-${identity}`;
  const glow = `home-logo-glow-${identity}`;
  return (
    <div className="home-logo" aria-hidden="true">
      <svg
        className="home-logo-glyph"
        viewBox="12 12 88 88"
        width="64"
        height="64"
        fill="none"
        focusable="false"
      >
        <defs>
          <linearGradient
            id={gradient}
            x1="19"
            y1="20"
            x2="93"
            y2="93"
            gradientUnits="userSpaceOnUse"
          >
            <stop stopColor="var(--home-logo-cyan)" />
            <stop offset="0.48" stopColor="var(--home-logo-blue)" />
            <stop offset="1" stopColor="var(--home-logo-violet)" />
          </linearGradient>
          <filter id={glow} x="-60%" y="-60%" width="220%" height="220%" colorInterpolationFilters="sRGB">
            <feGaussianBlur stdDeviation="2.2" />
          </filter>
        </defs>
        <g stroke={`url(#${gradient})`} fill="none">
          <circle className="home-logo-ring home-logo-ring-first" cx="73" cy="72" r="24" />
          <circle className="home-logo-ring home-logo-ring-second" cx="73" cy="72" r="24" />
          <path className="home-logo-aura" d={logoPath} pathLength="1" strokeWidth="11" strokeLinecap="round" filter={`url(#${glow})`} />
          <path
            className="home-logo-stroke"
            d={logoPath}
            pathLength="1"
            strokeWidth="7.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </g>
        <path
          className="home-logo-tracer home-logo-tracer-glow"
          d={ringPath}
          pathLength="1"
          stroke="var(--home-logo-highlight)"
          strokeWidth="7"
          strokeLinecap="round"
          filter={`url(#${glow})`}
        />
        <path
          className="home-logo-tracer"
          d={ringPath}
          pathLength="1"
          stroke="var(--home-logo-highlight)"
          strokeWidth="3.2"
          strokeLinecap="round"
        />
        <g className="home-logo-head" style={{ offsetPath: `path("${ringPath}")` }}>
          <circle r="7.5" fill="var(--home-logo-cyan)" opacity=".75" filter={`url(#${glow})`} />
          <circle r="3.8" fill="var(--home-logo-head-core)" stroke="var(--home-logo-violet)" strokeWidth="2.1" />
        </g>
      </svg>
    </div>
  );
}
