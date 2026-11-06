// The source remains editable Markdown; export produces a real OOXML deck.
export function presentationPages(source) {
  return source
    .trim()
    .split(/\r?\n\s*---\s*\r?\n/)
    .filter((s) => s.trim());
}
const plain = (s) =>
  s
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
export function presentationTitle(source) {
  return plain(source.match(/^#{1,6}\s+(.+)$/m)?.[1] || "演示文稿")
    .replace(/[\\/:*?"<>|]/g, " ")
    .slice(0, 80);
}
export async function createPresentation(source) {
  const { default: PptxGenJS } = await import("pptxgenjs");
  const pptx = new PptxGenJS();
  pptx.layout = "LAYOUT_WIDE";
  pptx.author = "LoopForge";
  pptx.subject = "会话生成的演示文稿";
  pptx.title = presentationTitle(source);
  pptx.lang = "zh-CN";
  pptx.theme = {
    headFontFace: "Microsoft YaHei",
    bodyFontFace: "Microsoft YaHei",
    lang: "zh-CN",
  };
  const pages = presentationPages(source);
  pages.forEach((page, index) => {
    const slide = pptx.addSlide();
    slide.background = { color: index === 0 ? "142B45" : "FAFBFC" };
    slide.addShape(pptx.ShapeType.rect, {
      x: 0,
      y: 0,
      w: 0.12,
      h: 7.5,
      fill: { color: "3D87C8" },
      line: { color: "3D87C8" },
    });
    const lines = page
      .trim()
      .split(/\r?\n/)
      .filter((l) => l.trim());
    const titleIndex = lines.findIndex((l) => /^#{1,6}\s/.test(l));
    const title = plain(
      titleIndex >= 0
        ? lines.splice(titleIndex, 1)[0].replace(/^#{1,6}\s+/, "")
        : `第 ${index + 1} 页`,
    );
    const color = index === 0 ? "FFFFFF" : "142B45";
    slide.addText(title, {
      x: 0.65,
      y: index === 0 ? 1.2 : 0.65,
      w: 12,
      h: 1.1,
      fontSize: index === 0 ? 36 : 29,
      bold: true,
      color,
      breakLine: false,
      fit: "shrink",
      margin: 0,
    });
    const runs = lines.map((line) => ({
      text: plain(line.replace(/^\s*(?:[-*+]\s+|#{1,6}\s+)/, "")),
      options: {
        breakLine: true,
        paraSpaceAfterPt: 14,
        ...(/^\s*[-*+]\s/.test(line)
          ? { bullet: { indent: 20 }, hanging: 5 }
          : {}),
      },
    }));
    if (runs.length)
      slide.addText(runs, {
        x: 0.72,
        y: index === 0 ? 2.75 : 2,
        w: 11.8,
        h: index === 0 ? 3.2 : 4.55,
        fontSize: index === 0 ? 22 : 21,
        color: index === 0 ? "D6E4F2" : "35485B",
        valign: "top",
        fit: "shrink",
        margin: 0,
      });
    slide.addText(`${index + 1} / ${pages.length}`, {
      x: 11.6,
      y: 7,
      w: 1,
      h: 0.2,
      fontSize: 10,
      color: index === 0 ? "B7C9DA" : "788A9C",
      align: "right",
      margin: 0,
    });
    slide.addNotes(page);
  });
  return pptx;
}
export async function exportPresentation(source) {
  const pptx = await createPresentation(source);
  await pptx.writeFile({ fileName: presentationTitle(source) + ".pptx" });
}
