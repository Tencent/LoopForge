import { expect, test, vi } from "vitest";

const render = vi.hoisted(() => vi.fn());
vi.mock("react-dom/client", () => ({
  createRoot: vi.fn(() => ({ render })),
}));
vi.mock("../src/App.jsx", () => ({ default: () => null }));

test("browser entrypoint mounts the application", async () => {
  document.body.innerHTML = '<div id="root"></div>';
  await import("../src/main.jsx");
  expect(render).toHaveBeenCalledOnce();
  const tree = render.mock.calls[0][0];
  expect(tree.type.getDerivedStateFromError(new Error("boom")).error.message).toBe("boom");
  const boundary = new tree.type({ children: "content" });
  boundary.state = { error: null };
  expect(boundary.render()).toBe("content");
  boundary.state = { error: new Error("boom") };
  expect(boundary.render().props.className).toBe("fatal-error");
});
