import { expect, test } from 'vitest';
import postcss from 'postcss';
import scopeChatCSS from '../scope-chat-css.js';

test('scopes document and themed rules to the workbench while preserving keyframes', async () => {
  const css = ':root{color:red} body,button{margin:0} [data-theme="dark"] .chat-shell{color:white} html[data-theme="dark"]{--surface:black} @keyframes fade{from{opacity:0}to{opacity:1}}';
  const result = await postcss([scopeChatCSS()]).process(css, { from: undefined });
  expect(result.css).toContain('#chatRoot{color:red}');
  expect(result.css).toContain('#chatRoot, #chatRoot button{margin:0}');
  expect(result.css).toContain('[data-theme="dark"] #chatRoot .chat-shell{color:white}');
  expect(result.css).toContain('[data-theme="dark"] #chatRoot{--surface:black}');
  expect(result.css).toContain('@keyframes fade{from{opacity:0}to{opacity:1}}');
});
