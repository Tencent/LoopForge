// Dotted numeric version comparison for runner versions, mirroring
// versionAtLeast in internal/domain/runner.go: a leading "v" is ignored,
// missing components count as 0, and malformed input is treated as older.
// String comparison is wrong here ('0.10.1' < '0.3.0' lexically).

const parse = v => {
  const parts = String(v ?? '').trim().replace(/^v/, '').split('.');
  const out = parts.map(p => (/^\d+$/.test(p) ? Number(p) : NaN));
  return out.some(Number.isNaN) ? null : out;
};

export function versionAtLeast(have, want) {
  const h = parse(have);
  if (!h) return false;
  const w = parse(want) || [];
  for (let i = 0; i < w.length; i++) {
    const hv = h[i] ?? 0;
    if (hv !== w[i]) return hv > w[i];
  }
  return true;
}
