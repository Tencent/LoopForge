export function slugify(raw: string): string {
  const s = raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s.slice(0, 32) || "res";
}

export function uniqueAlias(base: string, used: Set<string>): string {
  let alias = slugify(base);
  let i = 2;
  while (used.has(alias)) {
    alias = `${slugify(base)}-${i}`;
    i += 1;
  }
  used.add(alias);
  return alias;
}
