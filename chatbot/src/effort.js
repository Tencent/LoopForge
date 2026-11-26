const levels = {
  none: { label: "Tắt", icon: "sparkles", detail: "Không dùng suy luận thêm." },
  thinking: { label: "Bật", icon: "brain", detail: "Bật chế độ suy nghĩ." },
  minimal: { label: "Thấp nhất", icon: "effort-low", detail: "Suy luận tối thiểu, ưu tiên trả lời nhanh." },
  low: { label: "Thấp", icon: "effort-low", detail: "Ưu tiên tốc độ, phù hợp câu hỏi đơn giản." },
  medium: { label: "Trung bình", icon: "effort-medium", detail: "Cân bằng tốc độ và độ sâu suy luận." },
  high: { label: "Cao", icon: "brain", detail: "Suy luận kỹ hơn, phù hợp bài toán phức tạp." },
  xhigh: { label: "Cao hơn", icon: "search", detail: "Dành nhiều thời gian suy luận hơn, phù hợp việc khó." },
  max: { label: "Cao nhất", icon: "effort-max", detail: "Dùng cường độ suy nghĩ cao nhất mà model hỗ trợ; có thể mất nhiều thời gian hơn." },
  ultra: { label: "Cực đại", icon: "effort-max", detail: "Dùng cường độ suy luận cực đại; có thể mất nhiều thời gian hơn." },
  ultracode: { label: "Cực đại", icon: "effort-max", detail: "Dùng cường độ suy nghĩ ultracode của model; có thể mất nhiều thời gian hơn." },
};

export function modelEffort(model, value) {
  return model?.efforts?.includes(value) ? value : "";
}

export function knownEffort(value) {
  return typeof value === "string" && Object.hasOwn(levels, value) ? value : "";
}

export function effortOptions(model) {
  return [
    { id: "", label: "Mặc định", icon: "sparkles", detail: "Dùng cường độ suy nghĩ mặc định của model hiện tại." },
    ...(model?.efforts || []).filter(knownEffort).map((id) => ({ id, ...levels[id] })),
  ];
}
