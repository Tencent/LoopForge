// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const app = readFileSync(new URL("../src/App.jsx", import.meta.url), "utf8");

function rule(selector, source = styles) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\>]/g, "\\$&");
  const match = source.match(new RegExp(`(?:^|\\n)\\s*${escaped}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `missing rule for ${selector}`);
  return match[1];
}

function mediaBlock(query) {
  const start = styles.indexOf(`@media (${query})`);
  assert.notEqual(start, -1, `missing @media (${query})`);
  let depth = 0;
  for (let i = styles.indexOf("{", start); i < styles.length; i += 1) {
    if (styles[i] === "{") depth += 1;
    if (styles[i] === "}" && --depth === 0) return styles.slice(start, i + 1);
  }
  throw new Error(`unterminated @media (${query})`);
}

test("the title button fills the remaining width instead of stopping at 450px", () => {
  const button = rule(".conversation-title > button");
  assert.doesNotMatch(button, /max-width/);
  assert.match(button, /min-width:\s*0/);
  assert.match(button, /text-overflow:\s*ellipsis/);
  assert.match(rule(".conversation-title > button.title-typing"), /text-overflow:\s*clip/);
  assert.doesNotMatch(styles, /\.conversation-title[^{]*\{[^}]*max-width:\s*450px/);
});

test("only the title text shrinks; icon, pencil, project and badge keep their size", () => {
  assert.match(rule(".conversation-status"), /flex-shrink:\s*0/);
  assert.match(rule(".conversation-title > .icon-button"), /flex-shrink:\s*0/);
  assert.match(rule(".conversation-project"), /flex-shrink:\s*0/);
  assert.match(rule(".conversation-title > .preview-badge"), /flex-shrink:\s*0/);
});

test("the project name renders as a theme-aware capsule", () => {
  const project = rule(".conversation-project");
  assert.match(project, /border:\s*1px solid var\(--line\)/);
  assert.match(project, /background:\s*var\(--subtle\)/);
  assert.match(project, /color:\s*var\(--navigation-secondary/);
  assert.match(project, /border-radius:\s*999px/);
  assert.match(project, /padding:\s*1px 8px/);
  assert.match(project, /font-size:\s*11px/);
  assert.match(project, /max-width:\s*180px/);
  assert.match(project, /margin-right:\s*6px/);
  assert.doesNotMatch(project, /margin-left/);
  assert.match(project, /text-overflow:\s*ellipsis/);
});

test("an empty project name does not render an empty capsule", () => {
  assert.match(app, /\{projectName \? \(\s*<div className="menu-anchor conversation-project-anchor">/);
});

test("the project capsule is a button that opens the project switcher", () => {
  const actions = app.slice(app.indexOf('<div className="header-actions">'));
  const capsule = actions.slice(actions.indexOf('className="conversation-project"') - 200, actions.indexOf("<Participants session={chat.session}"));
  assert.match(capsule, /<button\s+ref=\{headerProjectPicker\}\s+type="button"/);
  assert.match(capsule, /aria-haspopup="listbox"/);
  assert.match(capsule, /aria-expanded=\{menu\?\.type === "header-project"\}/);
  assert.match(capsule, /<SelectMenu\s+open=\{menu\?\.type === "header-project"\}[\s\S]*onSelect=\{chooseProject\}[\s\S]*anchorRef=\{headerProjectPicker\}/);
  assert.match(rule(".conversation-project"), /cursor:\s*pointer/);
  assert.match(rule(".conversation-project > span"), /text-overflow:\s*ellipsis/);
});

test("the project capsule sits in header-actions just before the participants", () => {
  const title = app.slice(app.indexOf('<div className="conversation-title">'), app.indexOf('<div className="header-actions">'));
  assert.doesNotMatch(title, /conversation-project/);
  const actions = app.slice(app.indexOf('<div className="header-actions">'));
  const project = actions.indexOf('className="conversation-project"');
  const participants = actions.indexOf("<Participants session={chat.session}");
  assert.notEqual(project, -1);
  assert.ok(project < participants, "project capsule should precede Participants");
});

test("the title icon shows the same reply status as the sidebar", () => {
  assert.match(app, /import ConversationStatusIcon from "\.\/ConversationStatusIcon"/);
  assert.match(
    app,
    /<ConversationStatusIcon\s+status=\{sessionReplyStatus\(chat\.session, \{\s*generation: chat\.generation,\s*busy: chat\.busy,\s*\}\)\}/,
  );
  assert.doesNotMatch(app, /conversation-title-icon/);
  const icon = readFileSync(new URL("../src/ConversationStatusIcon.jsx", import.meta.url), "utf8");
  assert.match(icon, /data-conversation-status=\{status\.state\}/);
  assert.match(icon, /role="img"/);
  assert.match(icon, /aria-label=\{status\.label\}/);
  assert.match(icon, /title=\{status\.label\}/);
  assert.match(icon, /M21 12a8 8 0 0 1-11\.3 7\.3L4 20l\.7-5\.7A8 8 0 1 1 21 12z/);
  assert.equal(icon.match(/className="conversation-status-dot"/g)?.length, 3);
  assert.match(icon, /className="conversation-status-mark"/);
});

test("status icon colours follow the sidebar palette in both themes", () => {
  assert.match(rule(".conversation-status"), /color:\s*var\(--muted\)/);
  assert.match(rule(".conversation-status"), /transition:\s*color 200ms/);
  assert.match(rule(".conversation-status"), /width:\s*14px/);
  const colours = {
    running: ["#5273dc", "#9cafff"],
    failed: ["#ce4444", "#f38989"],
    interrupted: ["#b87819", "#e7b76b"],
  };
  for (const [state, [light, dark]] of Object.entries(colours)) {
    const selector = `.conversation-status[data-conversation-status="${state}"]`;
    assert.match(rule(selector), new RegExp(`color:\\s*${light}`));
    assert.match(rule(`[data-theme="dark"] ${selector}`), new RegExp(`color:\\s*${dark}`));
  }
});

test("running animates the dots; failed, interrupted and cancelled show the corner mark", () => {
  assert.match(rule(".conversation-status-dot"), /opacity:\s*0/);
  const running = rule('.conversation-status[data-conversation-status="running"] .conversation-status-dot');
  assert.match(running, /opacity:\s*1/);
  assert.match(running, /animation:\s*conversation-status-dot 1s/);
  assert.match(styles, /:nth-of-type\(2\)\s*\{\s*animation-delay:\s*0\.15s/);
  assert.match(styles, /:nth-of-type\(3\)\s*\{\s*animation-delay:\s*0\.3s/);
  assert.match(styles, /@keyframes conversation-status-dot[^@]*translateY\(-1\.6px\)/);
  const mark = rule(".conversation-status-mark");
  assert.match(mark, /width:\s*7px/);
  assert.match(mark, /box-shadow:\s*0 0 0 1\.5px var\(--surface\)/);
  assert.match(mark, /transform:\s*scale\(0\)/);
  for (const state of ["failed", "interrupted", "cancelled"]) {
    assert.match(
      styles,
      new RegExp(`\\.conversation-status\\[data-conversation-status="${state}"\\] \\.conversation-status-mark[^{]*\\{[^}]*transform:\\s*scale\\(1\\)`),
    );
  }
  assert.doesNotMatch(styles, /data-conversation-status="(idle|completed)"/);
});

test("reduced motion stops the status dot animation", () => {
  assert.match(
    styles,
    /@media \(prefers-reduced-motion: reduce\)\s*\{\s*\.conversation-status,\s*\.conversation-status-dot,\s*\.conversation-status-mark\s*\{\s*animation:\s*none/,
  );
});

test("the rename pencil keeps its slot but only shows on hover or focus", () => {
  assert.match(rule(".conversation-title > .icon-button"), /opacity:\s*0/);
  const reveal = styles.match(
    /\.conversation-title:hover > \.icon-button,\s*\.conversation-title:focus-within > \.icon-button\s*\{([^}]*)\}/,
  );
  assert.ok(reveal, "missing hover/focus-within reveal rule");
  assert.match(reveal[1], /opacity:\s*1/);
  assert.match(rule(".chat-shell .conversation-title > button.icon-button"), /opacity 150ms/);
  assert.match(mediaBlock("hover: none"), /\.conversation-title > \.icon-button\s*\{\s*opacity:\s*1/);
  assert.match(
    styles,
    /@media \(prefers-reduced-motion: reduce\)\s*\{\s*\.chat-shell \.conversation-title > button\.icon-button\s*\{\s*transition:\s*none/,
  );
});

test("read-only sessions still omit the pencil", () => {
  assert.match(app, /\{!chat\.readOnly \? \(\s*<IconButton\s+label="重命名会话"/);
});

test("the project switcher stays visible on laptop widths and hides only at phone width", () => {
  assert.doesNotMatch(mediaBlock("max-width: 1250px"), /conversation-project/);
  assert.match(mediaBlock("max-width: 520px"), /\.conversation-project-anchor\s*\{\s*display:\s*none/);
  assert.match(mediaBlock("max-width: 850px"), /\.conversation-title\s*\{\s*display:\s*none/);
});
