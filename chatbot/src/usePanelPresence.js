import { useEffect, useLayoutEffect, useRef, useState } from "react";

/** Keep a closing pane mounted until its 220ms CSS transition has finished. */
export function usePanelPresence(open, duration = 240) {
  const [reduced, setReduced] = useState(
    () => matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  const [presence, setPresence] = useState({
    mounted: open,
    phase: open ? "open" : "closed",
    animating: false,
  });
  const current = useRef(presence);
  current.current = presence;

  useEffect(() => {
    const media = matchMedia("(prefers-reduced-motion: reduce)");
    const change = () => setReduced(media.matches);
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);

  useLayoutEffect(() => {
    let firstFrame, secondFrame, timer;
    if (reduced) {
      setPresence({ mounted: open, phase: open ? "open" : "closed", animating: false });
      return;
    }
    const settle = () => {
      timer = window.setTimeout(() => {
        setPresence({ mounted: open, phase: open ? "open" : "closed", animating: false });
      }, duration);
    };
    if (open) {
      if (!current.current.mounted) {
        setPresence({ mounted: true, phase: "entering", animating: true });
        // Paint the zero-width starting frame before expanding the grid track.
        firstFrame = requestAnimationFrame(() => {
          secondFrame = requestAnimationFrame(() => {
            setPresence({ mounted: true, phase: "open", animating: true });
            settle();
          });
        });
      } else if (current.current.phase === "closing") {
        // Reverse the active CSS transition from its current interpolated width.
        setPresence({ mounted: true, phase: "open", animating: true });
        settle();
      }
    } else if (current.current.mounted) {
      setPresence({ mounted: true, phase: "closing", animating: true });
      settle();
    }
    return () => {
      cancelAnimationFrame(firstFrame);
      cancelAnimationFrame(secondFrame);
      clearTimeout(timer);
    };
  }, [open, duration, reduced]);

  return presence;
}
