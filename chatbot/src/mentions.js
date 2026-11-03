export const referenceTypes = [
  { type: "agent", label: "角色", hint: "咨询角色与职责" },
  { type: "issue", label: "需求", hint: "查找需求与验收条件" },
  { type: "sop", label: "SOP", hint: "引用工作流程" },
  { type: "run", label: "运行", hint: "查看任务执行情况" },
  { type: "repository", label: "仓库", hint: "添加代码仓库上下文" },
];
export function mentionAt(text, caret, references = []) {
  const before = text.slice(0, caret);
  const match = before.match(/@([^@\n]*)$/);
  if (!match || /[a-z0-9_.+-]/i.test(before[match.index - 1] || ""))
    return null;
  if (references.some((ref) => match[1].startsWith((ref.name || ref.id) + " ")))
    return null;
  return { start: match.index, end: caret, query: match[1] };
}
export function insertMention(text, range, reference) {
  const token = `@${reference.name || reference.id} `;
  return {
    text: text.slice(0, range.start) + token + text.slice(range.end),
    caret: range.start + token.length,
  };
}
