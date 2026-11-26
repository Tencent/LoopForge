export const referenceTypes = [
  { type: "agent", label: "Vai trò", hint: "Giao cho vai trò thực hiện" },
  { type: "skill", label: "Skill", hint: "Dùng skill này để làm" },
  { type: "issue", label: "Yêu cầu", hint: "Tìm yêu cầu và điều kiện nghiệm thu" },
  { type: "sop", label: "SOP", hint: "Trích dẫn quy trình làm việc" },
  { type: "run", label: "Lần chạy", hint: "Xem tình trạng thực thi tác vụ" },
  { type: "repository", label: "Repository", hint: "Thêm ngữ cảnh repository" },
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
