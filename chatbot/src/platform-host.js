// The platform owns routing and shared context. Standalone development can
// still render the workbench without loading the management shell.
export const platform = () => window.loopforgePlatform;
export const uiLabel = (zh, en) => window.lfT?.(zh, en) ?? zh;
export const initialChatContext = () => platform()?.initialChatContext() || {
  projectId: new URLSearchParams(location.search).get('projectId') || '',
  sessionId: new URLSearchParams(location.search).get('session') || '',
  issueId: new URLSearchParams(location.search).get('issueId') || '',
  quickAccess: new URLSearchParams(location.search).get('quickAccess') === '1',
};
export function navigate(view, options = {}) {
  if (platform()) return platform().navigate(view, options);
  location.href = `/${options.projectId ? '?projectId=' + encodeURIComponent(options.projectId) : ''}#/${view}${options.runId ? '/' + encodeURIComponent(options.runId) : options.action === 'create' ? '/__new__' : ''}`;
}
export function subscribe(type, handler) {
  return platform()?.subscribe(type, handler) || (() => {});
}
