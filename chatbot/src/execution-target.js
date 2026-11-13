export function executionLocked(session) {
  return !!(session?.executionTargetLocked || session?.messages?.length ||
    session?.runId || session?.workspaceRunId || session?.ownWorkspaceRunIds?.length ||
    session?.parentSessionId || session?.taskId);
}
