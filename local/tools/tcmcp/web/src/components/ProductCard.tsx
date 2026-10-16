import type { ProductMeta } from "../types";

const ICONS: Record<string, string> = {
  redis: "R",
  cdb: "C",
  tdsqlc: "T",
  tke: "K",
  cls: "L",
};

export function ProductCard({
  product,
  on,
  toolCount,
  onToggle,
}: {
  product: ProductMeta;
  on: boolean;
  toolCount: number;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      className={`product-card${on ? " on" : ""}`}
      style={{ ["--dot" as string]: product.color }}
      onClick={onToggle}
      aria-pressed={on}
    >
      <span className="product-mark">{ICONS[product.id] ?? product.name[0]}</span>
      <span className="product-copy">
        <span className="product-title">
          <strong>{product.name}</strong>
          <span className={`count-pill${on ? " on" : ""}`}>
            {on
              ? toolCount > 0
                ? `${toolCount}/${product.tools.length} 已就绪`
                : `${product.tools.length} tools · 待绑定`
              : `${product.tools.length} tools`}
          </span>
        </span>
        <span className="product-desc">{product.description}</span>
      </span>
      <span className={`check${on ? " on" : ""}`} aria-hidden>
        {on ? (
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
            <path d="M2.2 6.2 4.8 8.8 9.8 3.2" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        ) : null}
      </span>
    </button>
  );
}
