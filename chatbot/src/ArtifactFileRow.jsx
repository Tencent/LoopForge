import React from "react";
import HarnessIcon from "./HarnessIcon";
import { definitionTarget } from "./platform.js";

const icons = {
  html: "globe",
  slides: "monitor",
  ppt: "monitor",
  sop: "workflow",
  "merge-request": "branch",
  role: "user",
  code: "stage-ide",
};

export default function ArtifactFileRow({
  artifact,
  projectId,
  selected,
  onSelect,
}) {
  const icon = artifact.mimeType?.startsWith("image/")
    ? "image"
    : icons[artifact.kind] || "doc";
  const target = definitionTarget(artifact, projectId);
  const href = target?.href || (artifact.external ? artifact.url : "");
  const Row = href ? "a" : "button";
  const title = target
    ? `${artifact.name} · ${target.label}`
    : artifact.external
      ? `${artifact.name} · 在代码仓库打开`
      : artifact.name;

  return (
    <Row
      className={`artifact-row artifact-file-row ${selected ? "is-selected" : ""}`}
      title={title}
      aria-current={selected ? "true" : undefined}
      {...(href
        ? { href, target: "_blank", rel: "noopener noreferrer" }
        : { type: "button", onClick: () => onSelect(artifact) })}
    >
      <span className="artifact-file-icon">
        <HarnessIcon name={icon} size={16} />
      </span>
      <span className="artifact-file-copy">
        <strong>{artifact.name}</strong>
        <small>
          {target
            ? `${artifact.kind === "sop" ? "SOP" : "角色"} · ${artifact.asset?.status === "published" ? "已发布" : "已保存"}`
            : artifact.source}
          {artifact.size
            ? ` · ${artifact.size < 1024 ? `${artifact.size} B` : `${Math.ceil(artifact.size / 1024)} KB`}`
            : ""}
        </small>
      </span>
      <HarnessIcon
        name={href ? "preview-expand" : "chevron-right"}
        className="artifact-row-action"
        size={13}
      />
    </Row>
  );
}
