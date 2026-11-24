'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const ctx = {
  URL,
  state: { currentUser: { id: 1 } },
  AgentFlowAssistantModels: {
    catalog: () => ({
      engines: [
        { id: 'codebuddy', name: 'CodeBuddy', configured: false },
        { id: 'claude-code', name: 'Claude Code', configured: true },
        { id: 'codex', name: 'Codex', configured: true },
      ],
      models: [
        { id: 'default', name: 'CLI 默认模型', engine: 'codebuddy' },
        { id: 'glm-5.3-flash', name: 'GLM 5.3 Flash', engine: 'codebuddy' },
        { id: 'claude-code:claude-haiku-4-5', name: 'Claude Haiku 4.5', engine: 'claude-code' },
        { id: 'codex:gpt-4.1-mini', name: 'GPT-4.1 Mini', engine: 'codex' },
      ],
    }),
  },
  escapeHTML: value => String(value).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch])),
  document: { querySelector: () => null },
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], 'utf8') + '\n;globalThis.__mb={assistantPrefsUI,assistantPrefsFormHTML,assistantPrefsDirty,assistantPrefsSignedIn,assistantReviewCatalogData,validateAssistantPrefsDraft,assistantReviewApiProbeListHTML};', ctx);
const mb = ctx.__mb;

// Unconfigured engines (like CodeBuddy without a key) must not appear in the review CLI list.
const catData = mb.assistantReviewCatalogData();
assert.deepEqual(Array.from(catData.engines, e => e.id), ['claude-code', 'codex']);
assert.equal(catData.modelsByEngine.has('codebuddy'), false);

// 1. CLI mode rendering & backward compatibility when approvalReviewMode is omitted.
const html = mb.assistantPrefsFormHTML({
  proactiveRoles: true,
  proactiveSops: false,
  approvalReviewModel: 'claude-code:claude-haiku-4-5',
  instructions: '先给结论 <b>&',
});
assert.match(html, /role="switch" name="proactiveRoles" checked/);
assert.match(html, /role="switch" name="proactiveSops" >/);
assert.match(html, /id="assistantPrefsReviewMode"/);
assert.match(html, /<option value="follow" >跟随当前对话 CLI 和模型<\/option>/);
assert.match(html, /<option value="cli" selected>自定义 CLI 和模型<\/option>/);
assert.match(html, /<option value="api" >自定义 API 和模型<\/option>/);
assert.match(html, /id="assistantPrefsReviewModel" name="approvalReviewModel" value="claude-code:claude-haiku-4-5"/);
assert.match(html, /id="assistantPrefsReviewCliBtn"[^>]*data-active-cli="claude-code"/);
assert.match(html, /\/images\/credentials\/claude\.svg/);
assert.match(html, /data-pick-cli="claude-code"/);
assert.match(html, /data-pick-cli="codex"/);
assert.match(html, /id="assistantPrefsReviewModelSearch"/);
assert.match(html, /data-pick-model="claude-code:claude-haiku-4-5"/);
assert.match(html, /id="assistantPrefsRefreshCli"/);
assert.match(html, /id="assistantPrefsManageCli"/);
assert.doesNotMatch(html, /CodeBuddy/, 'unconfigured CodeBuddy must not render in CLI picker');
assert.doesNotMatch(html, /glm-5\.3-flash/, 'unconfigured CodeBuddy models must not render in CLI picker');
assert.match(html, /id="assistantPrefsReviewApiBox" class="asst-review-sub" hidden/);
assert.match(html, /maxlength="4000"/);
assert.match(html, />先给结论 &lt;b&gt;&amp;<\/textarea>/, 'rules must be escaped inside the textarea');
assert.match(html, /9 \/ 4000/);
assert.match(html, /id="assistantPrefsSave" disabled/);
assert.match(html, /<option value="queue" selected>排队<\/option>/, 'queue remains the default');
assert.match(mb.assistantPrefsFormHTML({ steerOnSend: true }), /<option value="steer" selected>立即发送<\/option>/);

// 2. Follow mode hides both sub-panels.
const followHtml = mb.assistantPrefsFormHTML({ approvalReviewMode: 'follow', instructions: '' });
assert.match(followHtml, /<option value="follow" selected>跟随当前对话 CLI 和模型<\/option>/);
assert.match(followHtml, /id="assistantPrefsReviewCliBox" class="asst-review-sub" hidden/);
assert.match(followHtml, /id="assistantPrefsReviewApiBox" class="asst-review-sub" hidden/);

// 3. Custom API mode renders protocol/baseUrl/model/key fields + live model probe trigger without leaking raw secret.
const apiHtml = mb.assistantPrefsFormHTML({
  approvalReviewMode: 'api',
  approvalReviewApi: {
    protocol: 'gemini',
    baseUrl: 'https://generativelanguage.googleapis.com',
    model: 'gemini-2.5-flash',
    apiKeyConfigured: true,
  },
  instructions: '',
});
assert.match(apiHtml, /<option value="api" selected>自定义 API 和模型<\/option>/);
assert.match(apiHtml, /id="assistantPrefsReviewApiBox" class="asst-review-sub">/);
assert.match(apiHtml, /value="gemini" selected>Google Gemini/);
assert.match(apiHtml, /name="approvalReviewApiModel"[^>]*value="gemini-2\.5-flash"/);
assert.match(apiHtml, /placeholder="已配置，留空保持不变"/);
assert.match(apiHtml, /id="assistantPrefsApiKeyClearBtn"/);
assert.match(apiHtml, /id="assistantPrefsApiProbeBtn"[^>]*>获取可用模型<\/button>/);

const probeHtml = mb.assistantReviewApiProbeListHTML([
  { id: 'gemini-2.5-flash', name: 'Gemini 2.5 Flash' },
  { id: 'gpt-4.1-mini', name: 'gpt-4.1-mini' },
], 'gemini-2.5-flash');
assert.match(probeHtml, /共 2 个可用模型/);
assert.match(probeHtml, /data-api-model="gemini-2\.5-flash"/);
assert.match(probeHtml, /class="asst-probe-option is-selected" data-api-model="gemini-2\.5-flash"/);
assert.match(probeHtml, /data-api-model="gpt-4\.1-mini"/);

// 4. Dirty tracking across modes & send behavior.
const form = {
  proactiveRoles: { checked: true },
  proactiveSops: { checked: false },
  messageDuringGeneration: { value: 'queue' },
  approvalReviewMode: { value: 'cli' },
  approvalReviewModel: { value: '  claude-code:claude-haiku-4-5  ' },
  approvalReviewApiProtocol: { value: 'chat' },
  approvalReviewApiBaseUrl: { value: '' },
  approvalReviewApiModel: { value: '' },
  approvalReviewApiKey: { value: '' },
  approvalReviewApiKeyClear: { value: '0' },
  instructions: { value: '  先给结论  ' },
};
mb.assistantPrefsUI.saved = {
  proactiveRoles: true,
  proactiveSops: false,
  approvalReviewMode: 'cli',
  approvalReviewModel: 'claude-code:claude-haiku-4-5',
  instructions: '先给结论',
};
assert.equal(mb.assistantPrefsDirty(form), false, 'surrounding whitespace is not a change');
form.messageDuringGeneration.value = 'steer';
assert.equal(mb.assistantPrefsDirty(form), true, 'send behavior changes must be saved');
form.messageDuringGeneration.value = 'queue';
form.proactiveSops.checked = true;
assert.equal(mb.assistantPrefsDirty(form), true);
form.proactiveSops.checked = false;
form.approvalReviewModel.value = 'codex:gpt-4.1-mini';
assert.equal(mb.assistantPrefsDirty(form), true);
form.approvalReviewModel.value = 'claude-code:claude-haiku-4-5';
form.approvalReviewMode.value = 'follow';
assert.equal(mb.assistantPrefsDirty(form), true);
form.approvalReviewMode.value = 'cli';
form.instructions.value = '先给结论，再给细节';
assert.equal(mb.assistantPrefsDirty(form), true);

// 5. Validation in custom API mode (including cross-origin Base URL key reuse guard).
assert.ok(mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'chat', model: '', apiKey: 'sk-1' } }, null));
assert.ok(mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'chat', model: 'gpt-4.1-mini', apiKey: '' } }, null));
assert.ok(mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'chat', model: 'gpt-4.1-mini', baseUrl: 'ftp://bad.example', apiKey: 'sk-1' } }, null));
const savedApi = { approvalReviewApi: { protocol: 'chat', baseUrl: 'https://gateway.example.com/v1', model: 'gpt-4.1-mini', apiKeyConfigured: true } };
assert.equal(mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'chat', model: 'gpt-4.1-mini', baseUrl: 'https://gateway.example.com/v1', apiKey: '' } }, savedApi), null);
assert.equal(mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'responses', model: 'gpt-5.4-mini', baseUrl: 'https://gateway.example.com/v2', apiKey: '' } }, savedApi), null, 'same-origin path/protocol change can reuse saved key');
const crossOriginErr = mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'chat', model: 'gpt-4.1-mini', baseUrl: 'https://other.example.com/v1', apiKey: '' } }, savedApi);
assert.ok(crossOriginErr && crossOriginErr.message.includes('跨源变更'), 'cross-origin Base URL change must require new API key');
const officialOriginChangeErr = mb.validateAssistantPrefsDraft({ approvalReviewMode: 'api', approvalReviewApi: { protocol: 'messages', model: 'claude-haiku-4-5', baseUrl: '', apiKey: '' } }, { approvalReviewApi: { protocol: 'chat', baseUrl: '', apiKeyConfigured: true } });
assert.ok(officialOriginChangeErr && officialOriginChangeErr.message.includes('跨源变更'), 'switching official endpoint across providers must require new API key');

ctx.state.currentUser = { id: 0, isGuest: true };
assert.equal(mb.assistantPrefsSignedIn(), false);
