/* Hover ink adapted from Grasp's hoverInkDirective.ts / hoverInkGeometry.ts.
 * MIT copyright and license notice reproduced in hover-ink.css.
 */
(function () {
  'use strict';
  if (typeof document === 'undefined' || typeof MutationObserver !== 'function' || window.AgentFlowHoverInk) return;

  const selector = '.platform-sidebar button.nav, .chat-shell button.new-chat-button, .chat-shell button.session-link, .chat-shell button.session-project-heading, .chat-shell :is(button, a)[data-hover-ink-control], #sopsView :is(button, a)[data-hover-ink-control], .settings-navigation-toggle, .credential-ink';
  const duration = 350;
  const hoverPointer = window.matchMedia('(hover: hover) and (pointer: fine)');
  const states = new WeakMap();
  const active = new Set();

  function hostFor(target) {
    return typeof Element !== 'undefined' && target instanceof Element ? target.closest(selector) : null;
  }

  function isBlocked(host) {
    return host.matches(':disabled, [disabled], [aria-disabled="true"], [aria-busy="true"], [data-loading="true"], .loading, .is-loading') || !!host.closest('[inert]');
  }

  function stateFor(host) {
    let state = states.get(host);
    if (!state) {
      state = { pointer: false, focused: false, phase: 'idle', timer: null };
      states.set(host, state);
    }
    return state;
  }

  function clearTimer(state) {
    if (state.timer !== null) window.clearTimeout(state.timer);
    state.timer = null;
  }

  function place(host, clientX, clientY) {
    const rect = host.getBoundingClientRect();
    const x = clientX === undefined ? rect.width / 2 : Math.max(0, Math.min(rect.width, clientX - rect.left));
    const y = clientY === undefined ? rect.height / 2 : Math.max(0, Math.min(rect.height, clientY - rect.top));
    const diameter = Math.hypot(Math.max(x, rect.width - x), Math.max(y, rect.height - y)) * 2;
    host.style.setProperty('--ink-x', x + 'px');
    host.style.setProperty('--ink-y', y + 'px');
    host.style.setProperty('--ink-d', diameter + 'px');
  }

  function suppress(host) {
    const state = stateFor(host);
    clearTimer(state);
    state.phase = 'idle';
    host.dataset.hoverInk = 'suppressed';
    host.style.setProperty('--ink-d', '0px');
    active.delete(host);
  }

  function expand(host, clientX, clientY) {
    if (isBlocked(host)) return suppress(host);
    const state = stateFor(host);
    clearTimer(state);
    place(host, clientX, clientY);
    state.phase = 'hover';
    host.dataset.hoverInk = 'hover';
    active.add(host);
  }

  function retract(host, force) {
    const state = states.get(host);
    if (!state || (!force && (state.pointer || state.focused))) return;
    if (isBlocked(host)) return suppress(host);
    clearTimer(state);
    state.phase = 'leave';
    host.dataset.hoverInk = 'leave';
    state.timer = window.setTimeout(function () {
      state.timer = null;
      if (state.phase !== 'leave') return;
      state.phase = 'idle';
      host.dataset.hoverInk = 'idle';
      host.style.setProperty('--ink-d', '0px');
      active.delete(host);
    }, duration);
  }

  document.addEventListener('pointerover', function (event) {
    const host = hostFor(event.target);
    if (!host || host.contains(event.relatedTarget) || event.pointerType === 'touch' || !hoverPointer.matches) return;
    const state = stateFor(host);
    state.pointer = true;
    state.x = event.clientX;
    state.y = event.clientY;
    expand(host, state.x, state.y);
  });

  document.addEventListener('pointerout', function (event) {
    const host = hostFor(event.target);
    if (!host || host.contains(event.relatedTarget)) return;
    stateFor(host).pointer = false;
    retract(host);
  });

  document.addEventListener('pointerdown', function (event) {
    const host = hostFor(event.target);
    if (!host) return;
    const state = stateFor(host);
    if (event.pointerType === 'touch' || !hoverPointer.matches || state.phase !== 'hover') {
      expand(host, event.clientX, event.clientY);
    }
  });

  function releasePointer(event) {
    const host = hostFor(event.target);
    if (!host) return;
    const state = stateFor(host);
    if (event.type === 'pointercancel') {
      state.pointer = false;
      state.focused = false;
      retract(host, true);
    } else if (event.pointerType === 'touch' || !hoverPointer.matches) {
      // Finish the expansion before retracting; touch must never leave ink open.
      clearTimer(state);
      state.timer = window.setTimeout(function () {
        state.pointer = false;
        state.focused = false;
        retract(host, true);
      }, duration);
    }
  }
  document.addEventListener('pointerup', releasePointer);
  document.addEventListener('pointercancel', releasePointer);

  document.addEventListener('focusin', function (event) {
    const host = hostFor(event.target);
    if (!host || !host.matches(':focus-visible')) return;
    stateFor(host).focused = true;
    expand(host);
  });

  document.addEventListener('focusout', function (event) {
    const host = hostFor(event.target);
    if (!host || host.contains(event.relatedTarget)) return;
    stateFor(host).focused = false;
    retract(host);
  });

  document.addEventListener('keydown', function (event) {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const host = hostFor(event.target);
    if (!host || event.repeat) return;
    stateFor(host).focused = true;
    expand(host);
  });

  function resetActive() {
    active.forEach(function (host) {
      const state = stateFor(host);
      state.pointer = false;
      state.focused = false;
      retract(host, true);
    });
  }
  window.addEventListener('blur', resetActive);
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') resetActive();
  });

  // React may update classes while the pointer stays over the same button.
  // Only attributes carry the ink state; no React-managed child nodes are added.
  const observer = new MutationObserver(function (mutations) {
    const changed = new Set();
    mutations.forEach(function (mutation) {
      const host = hostFor(mutation.target);
      if (host && states.has(host)) changed.add(host);
    });
    changed.forEach(function (host) {
      const state = stateFor(host);
      if (isBlocked(host)) suppress(host);
      else if (host.dataset.hoverInk === 'suppressed') {
        if (state.pointer) expand(host, state.x, state.y);
        else if (state.focused) expand(host);
        else host.dataset.hoverInk = 'idle';
      }
    });
    active.forEach(function (host) {
      if (!host.isConnected) {
        clearTimer(stateFor(host));
        active.delete(host);
      } else if (isBlocked(host)) suppress(host);
    });
  });
  observer.observe(document.documentElement, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: ['class', 'disabled', 'aria-disabled', 'aria-busy', 'data-loading', 'inert'],
  });

  window.AgentFlowHoverInk = { reset: resetActive };
}());
