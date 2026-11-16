'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const ctx = {
  state: { currentUser: { id: 1 } },
  escapeHTML: value => String(value).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch])),
  document: { querySelector: () => null },
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], 'utf8') + '\n;globalThis.__mb={assistantPrefsUI,assistantPrefsFormHTML,assistantPrefsDirty,assistantPrefsSignedIn};', ctx);
const mb = ctx.__mb;

const html = mb.assistantPrefsFormHTML({ proactiveRoles: true, proactiveSops: false, instructions: '先给结论 <b>&' });
assert.match(html, /role="switch" name="proactiveRoles" checked/);
assert.match(html, /role="switch" name="proactiveSops" >/);
assert.match(html, /maxlength="4000"/);
assert.match(html, />先给结论 &lt;b&gt;&amp;<\/textarea>/, 'rules must be escaped inside the textarea');
assert.match(html, /9 \/ 4000/);
assert.match(html, /id="assistantPrefsSave" disabled/);

const form = { proactiveRoles: { checked: true }, proactiveSops: { checked: false }, instructions: { value: '  先给结论  ' } };
mb.assistantPrefsUI.saved = { proactiveRoles: true, proactiveSops: false, instructions: '先给结论' };
assert.equal(mb.assistantPrefsDirty(form), false, 'surrounding whitespace is not a change');
form.proactiveSops.checked = true;
assert.equal(mb.assistantPrefsDirty(form), true);
form.proactiveSops.checked = false;
form.instructions.value = '先给结论，再给细节';
assert.equal(mb.assistantPrefsDirty(form), true);

ctx.state.currentUser = { id: 0, isGuest: true };
assert.equal(mb.assistantPrefsSignedIn(), false);
