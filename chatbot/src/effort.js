const levels = {
  low: { label: "低", icon: "effort-low", detail: "优先响应速度，适合简单问题。" },
  medium: { label: "中", icon: "effort-medium", detail: "兼顾响应速度与推理深度。" },
  high: { label: "高", icon: "brain", detail: "更充分地推理，适合复杂问题。" },
  xhigh: { label: "更高", icon: "search", detail: "投入更多推理时间，适合困难任务。" },
  max: { label: "最高", icon: "effort-max", detail: "使用模型支持的最高思考强度，耗时可能更长。" },
};

export function modelEffort(model, value) {
  return model?.efforts?.includes(value) ? value : "";
}

export function effortOptions(model) {
  return [
    { id: "", label: "默认", icon: "sparkles", detail: "使用当前模型的默认思考强度。" },
    ...(model?.efforts || []).filter((id) => levels[id]).map((id) => ({ id, ...levels[id] })),
  ];
}
