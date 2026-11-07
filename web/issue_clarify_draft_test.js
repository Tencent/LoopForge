'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[2], 'utf8');

function extract(name) {
  const start = source.search(new RegExp('^function ' + name + '\\(', 'm'));
  assert.ok(start >= 0, name);
  const rest = source.slice(start);
  const next = /\nfunction \w+\(/.exec(rest.slice(rest.indexOf('\n') + 1));
  return next ? rest.slice(0, rest.indexOf('\n') + 1 + next.index) : rest;
}

const renderStart = source.indexOf('function renderIssueDetailTab()');
const renderBody = source.slice(renderStart, source.indexOf('\nfunction drawerFact(', renderStart));
const redraw=renderBody.indexOf('host.innerHTML=panes');
assert.ok(redraw>0&&renderBody.indexOf('captureIssueClarifyDraft')<redraw);
assert.ok(renderBody.includes("host.querySelector?.('.clarify-workspace')"));
assert.ok(renderBody.includes('restoreIssueClarifyDraft(host,clarifyDraft)'));

function classes(initial) {
  const values = new Set(initial || []);
  return {
    contains: v => values.has(v),
    add: v => values.add(v),
    remove: v => values.delete(v),
    toggle(v, on) {
      if (on === undefined) on = !values.has(v);
      if (on) values.add(v); else values.delete(v);
      return on;
    }
  };
}
function el(spec) {
  const node = {
    id: spec.id || '',
    tag: spec.tag || '',
    textContent: spec.text || '',
    value: spec.value || '',
    classList: classes(spec.classes),
    selectionStart: spec.selectionStart,
    selectionEnd: spec.selectionEnd,
    scrollTop: spec.scrollTop || 0,
    children: spec.children || [],
    focused: false,
    focus() { this.focused = true; },
    setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; },
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
    querySelectorAll(selector) {
      const walk = nodes => nodes.flatMap(child => [child, ...walk(child.children || [])]);
      return walk(this.children).filter(child => matches(child, selector));
    },
    contains(other) { return other === this || this.querySelectorAll('*').includes(other); }
  };
  return node;
}
function matches(node, selector) {
  if (selector === '*') return true;
  if (selector === 'b') return node.tag === 'b';
  if (selector.startsWith('#')) return node.id === selector.slice(1);
  if (selector.startsWith('.')) return node.classList.contains(selector.slice(1));
  return false;
}

const active = {current: null};
const flags = {analyzing: false};
const ctx = {
  document: {get activeElement() { return active.current; }},
  issueClarifying: () => flags.analyzing,
  requirementNeedsClarification: () => false,
  clarificationNeedsConfirm: () => false,
  issueExecutionBlocked: () => '',
  clarificationPolicy: () => ({threshold: 80}),
  clarifyQuestionOptions: q => q.options || [],
  escapeHTML: s => String(s ?? '').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c])),
  CLARIFY_AUTO_PROMPTS: ['auto'],
  submittedClarifyDraft: null
};
vm.createContext(ctx);
for (const name of ['issueClarifyViewKey', 'clarifyQuestionKey', 'clarifySubmittedHTML', 'clarifyQuestionCardsHTML', 'clarifyAnswerDraft', 'captureIssueClarifyDraft', 'restoreIssueClarifySelection', 'restoreIssueClarifyDraft']) {
  vm.runInContext(extract(name), ctx);
}

const issue = {
  id: 'dns-1', source: 'tapd', updatedAt: '2026-10-14T02:55:07Z',
  clarification: {
    status: 'in_progress', score: 42,
    questions: [{question: '预计涉及多少行？', options: ['50万行']}],
    history: [{role: 'assistant', content: '{}'}]
  }
};
const key = ctx.issueClarifyViewKey(issue, {id: 'p'});
const refreshed = JSON.parse(JSON.stringify(issue));
refreshed.updatedAt = '2026-10-14T03:01:00Z';
assert.equal(ctx.issueClarifyViewKey(refreshed, {id: 'p'}), key);
refreshed.clarification.score = 80;
assert.notEqual(ctx.issueClarifyViewKey(refreshed, {id: 'p'}), key);

const other = el({classes: ['issue-clarify-other'], value: '凌晨 2-5 点，约 50 万行', selectionStart: 2, selectionEnd: 2});
const option = el({classes: ['issue-clarify-option'], text: '50万行'});
option.classList.add('selected');
const question = el({classes: ['issue-clarify-question'], children: [
  el({tag: 'b', text: '1. 预计涉及多少行？'}),
  option,
  other
]});
const extra = el({id: 'issueClarifyInput', value: '补充：低峰执行', selectionStart: 3, selectionEnd: 5});
const host = el({classes: ['clarify-workspace'], scrollTop: 40, children: [question, extra]});
active.current = other;
const draft = ctx.captureIssueClarifyDraft(host);
assert.equal(draft.extra, '补充：低峰执行');
assert.equal(draft.questions[0].text, '1. 预计涉及多少行？');
assert.equal(draft.questions[0].other, '凌晨 2-5 点，约 50 万行');
assert.equal(draft.questions[0].selected.length, 1);
assert.equal(draft.questions[0].selected[0], true);
assert.equal(draft.focus.kind, 'other');

other.value = '';
extra.value = '';
option.classList.remove('selected');
host.scrollTop = 0;
active.current = null;
ctx.restoreIssueClarifyDraft(host, draft);
assert.equal(other.value, '凌晨 2-5 点，约 50 万行');
assert.equal(extra.value, '补充：低峰执行');
assert.equal(option.classList.contains('selected'), true);
assert.equal(host.scrollTop, 40);
assert.equal(other.focused, true);
assert.equal(other.selectionStart, 2);

const nextQuestion = el({classes: ['issue-clarify-question'], children: [
  el({tag: 'b', text: '1. 新的问题'}),
  el({classes: ['issue-clarify-other'], value: ''})
]});
const nextHost = el({children: [nextQuestion, el({id: 'issueClarifyInput', value: ''})]});
ctx.restoreIssueClarifyDraft(nextHost, draft);
assert.equal(nextHost.querySelector('.issue-clarify-other').value, '');
assert.equal(nextHost.querySelector('#issueClarifyInput').value, '');

const cards = ctx.clarifyQuestionCardsHTML(issue.clarification.questions, {questions:[{selected:[true], other:'凌晨'}]});
assert.ok(cards.includes('issue-clarify-option selected'));
assert.ok(cards.includes('value="凌晨"'));
assert.equal(ctx.clarifySubmittedHTML(''), '');
assert.equal(ctx.clarifySubmittedHTML('auto'), '');
assert.ok(ctx.clarifySubmittedHTML('50万行，凌晨执行').includes('本轮回答已提交'));
assert.ok(ctx.clarifySubmittedHTML('50万行，凌晨执行').includes('50万行，凌晨执行'));

ctx.submittedClarifyDraft = {
  issueId: 'dns-1',
  questionKey: ctx.clarifyQuestionKey(issue.clarification.questions),
  questions: [{selected:[true], other:'凌晨'}],
  extra: '补充',
  reply: '50万行，凌晨执行'
};
flags.analyzing = true;
assert.equal(ctx.clarifyAnswerDraft(issue).reply, '50万行，凌晨执行');
issue.clarification.questions = [{question:'新问题', options:['甲','乙']}];
assert.equal(ctx.clarifyAnswerDraft(issue).reply, '50万行，凌晨执行');
flags.analyzing = false;
assert.equal(ctx.clarifyAnswerDraft(issue), null);
assert.equal(ctx.submittedClarifyDraft, null);

const paneStart = source.indexOf('function clarifyPane(');
const pane = source.slice(paneStart, source.indexOf('\nfunction latestClarifyParsed(', paneStart));
assert.ok(pane.includes("analyzing?'':clarifyQuestionCardsHTML"));
assert.ok(pane.includes('clarifySubmittedHTML(saved?.reply)'));
const sendStart = source.indexOf('async function sendIssueClarification(');
const send = source.slice(sendStart, source.indexOf('\nasync function retryIssueClarificationWriteBack(', sendStart));
assert.ok(send.includes('rememberSubmittedClarifyDraft(issue,msg)'));
assert.ok(send.indexOf('issueClarifyJobs.set') < send.indexOf('renderIssueDetailTab()'));
assert.ok(!pane.includes('class="issue-clarify-option" data-multi'));
