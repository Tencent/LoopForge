(function () {
  'use strict';

  const cloneAttribute = 'data-af-sidebar-motion-clone';

  function globalRect(element) {
    const bounds = element.getBoundingClientRect();
    const rect = { left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height };
    let view = element.ownerDocument.defaultView;
    while (view && view !== window && view.frameElement) {
      const frame = view.frameElement;
      const frameRect = frame.getBoundingClientRect();
      const scaleX = frame.offsetWidth ? frameRect.width / frame.offsetWidth : 1;
      const scaleY = frame.offsetHeight ? frameRect.height / frame.offsetHeight : 1;
      rect.left = frameRect.left + (frame.clientLeft + rect.left) * scaleX;
      rect.top = frameRect.top + (frame.clientTop + rect.top) * scaleY;
      rect.width *= scaleX;
      rect.height *= scaleY;
      view = frame.ownerDocument.defaultView;
    }
    return rect;
  }

  function copyStyle(target, computed) {
    const declarations = [];
    // Resolved shorthands preserve the navigation's painted layout without
    // serializing hundreds of unrelated browser defaults on every icon path.
    const svg = target.namespaceURI === 'http://www.w3.org/2000/svg';
    const properties = svg
      ? (copyStyle.svgProperties ||= ('display position inset width height min-width min-height max-width max-height box-sizing margin padding flex flex-shrink align-self vertical-align color visibility opacity transform transform-origin transform-box clip-path filter overflow fill fill-opacity fill-rule stroke stroke-width stroke-opacity stroke-linecap stroke-linejoin stroke-miterlimit stroke-dasharray stroke-dashoffset paint-order vector-effect shape-rendering').split(' '))
      : (copyStyle.properties ||= ('display position inset z-index float clear box-sizing width height min-width min-height max-width max-height aspect-ratio margin padding border-width border-style border-color border-radius border-image background box-shadow outline outline-offset opacity visibility color font font-variant font-feature-settings font-variation-settings font-kerning line-height letter-spacing word-spacing text-align text-align-last text-indent text-transform text-decoration text-shadow text-overflow text-rendering white-space word-break overflow-wrap hyphens tab-size direction writing-mode unicode-bidi vertical-align -webkit-text-fill-color -webkit-text-stroke -webkit-line-clamp -webkit-box-orient list-style overflow overflow-clip-margin scrollbar-gutter scrollbar-width scrollbar-color overscroll-behavior flex flex-direction flex-wrap order align-items align-content align-self justify-content justify-items justify-self gap grid-template-columns grid-template-rows grid-auto-flow grid-auto-columns grid-auto-rows grid-column grid-row transform transform-origin transform-box clip-path filter backdrop-filter mix-blend-mode isolation contain content-visibility appearance accent-color object-fit object-position').split(' '));
    for (const property of properties) {
      const value = computed.getPropertyValue(property);
      if (value) declarations.push(property + ':' + value + '!important');
    }
    declarations.push('animation:none!important', 'transition:none!important', 'pointer-events:none!important', 'caret-color:transparent!important');
    target.style.cssText = declarations.join(';');
  }

  function pseudoText(content) {
    // Tree connectors use empty content; retain literal labels used by other pseudo elements.
    return content.replace(/^(['"])([\s\S]*)\1$/, '$2').replace(/\\([0-9a-f]{1,6})\s?|\\(.)/gi,
      (_, hex, character) => hex ? String.fromCodePoint(parseInt(hex, 16)) : character);
  }

  function capture(nav, geometry) {
    if (!nav) return null;
    const rect = globalRect(nav);
    const element = nav.cloneNode(true);
    const originals = [nav, ...nav.querySelectorAll('*')];
    const copies = [element, ...element.querySelectorAll('*')];
    const scrollPositions = [];
    originals.forEach((original, index) => {
      const copy = copies[index];
      const view = original.ownerDocument.defaultView;
      copyStyle(copy, view.getComputedStyle(original));
      copy.setAttribute(cloneAttribute, '');
      for (const attribute of [...copy.attributes]) {
        if (attribute.name === 'id' || attribute.name === 'autofocus' || /^on/i.test(attribute.name)) copy.removeAttribute(attribute.name);
      }
      if (original === nav || original.scrollHeight > original.clientHeight || original.scrollWidth > original.clientWidth || original.scrollTop || original.scrollLeft) {
        scrollPositions.push({ element: copy, top: original.scrollTop, left: original.scrollLeft });
      }
      if ('value' in original && 'value' in copy) copy.value = original.value;
      for (const pseudo of ['::before', '::after']) {
        const style = view.getComputedStyle(original, pseudo);
        const content = style.content;
        if (!content || content === 'none' || content === 'normal' || style.display === 'none') continue;
        const replacement = document.createElement('span');
        replacement.setAttribute(cloneAttribute, '');
        replacement.setAttribute('aria-hidden', 'true');
        copyStyle(replacement, style);
        replacement.textContent = pseudoText(content);
        if (pseudo === '::before') copy.prepend(replacement);
        else copy.append(replacement);
      }
    });
    element.inert = true;
    element.setAttribute('aria-hidden', 'true');
    return { element, rect, geometry: { ...rect, ...geometry }, scrollPositions };
  }

  function play(from, to, options = {}) {
    if (!from || !to) return { finished: Promise.resolve(), cancel() {}, reverse() {} };
    const geometry = to.geometry;
    const viewport = document.createElement('div');
    viewport.setAttribute('data-sidebar-mode-motion', '');
    viewport.setAttribute('aria-hidden', 'true');
    viewport.inert = true;
    Object.assign(viewport.style, {
      position: 'fixed', left: geometry.left + 'px', top: geometry.top + 'px',
      width: geometry.width + 'px', height: geometry.height + 'px',
      overflow: 'hidden', borderRadius: '0 0 15px 15px', zIndex: '1000', pointerEvents: 'none',
      background: geometry.background || 'transparent', isolation: 'isolate',
    });
    const style = document.createElement('style');
    style.textContent = `[${cloneAttribute}]::before,[${cloneAttribute}]::after{content:none!important;display:none!important}`;
    viewport.append(style);

    function pane(snapshot, name) {
      const wrapper = document.createElement('div');
      wrapper.setAttribute('data-mode-pane', name);
      Object.assign(wrapper.style, { position: 'absolute', inset: '0', pointerEvents: 'none', willChange: 'transform, opacity' });
      const root = snapshot.element;
      const layout = {
        position: 'absolute', left: (snapshot.rect.left - geometry.left) + 'px',
        top: (snapshot.rect.top - geometry.top) + 'px', right: 'auto', bottom: 'auto',
        width: snapshot.rect.width + 'px', height: snapshot.rect.height + 'px',
        margin: '0', boxSizing: 'border-box', transform: 'none',
      };
      for (const [property, value] of Object.entries(layout)) root.style.setProperty(property.replace(/[A-Z]/g, letter => '-' + letter.toLowerCase()), value, 'important');
      wrapper.append(root);
      viewport.append(wrapper);
      return wrapper;
    }

    const outgoing = pane(from, 'from');
    const incoming = pane(to, 'to');
    document.body.append(viewport);
    for (const snapshot of [from, to]) for (const scroll of snapshot.scrollPositions) {
      scroll.element.scrollTop = scroll.top;
      scroll.element.scrollLeft = scroll.left;
    }
    const sign = options.direction === 'settings' ? -1 : 1;
    const duration = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : (options.duration ?? 320);
    const transforms = { duration, easing: 'cubic-bezier(.22,1,.36,1)', fill: 'both' };
    const opacity = { duration, easing: 'ease', fill: 'both' };
    const animations = [
      outgoing.animate([{ transform: 'translateX(0)' }, { transform: `translateX(${sign * 100}%)` }], transforms),
      incoming.animate([{ transform: `translateX(${-sign * 100}%)` }, { transform: 'translateX(0)' }], transforms),
      outgoing.animate([{ opacity: 1 }, { opacity: .4 }], opacity),
      incoming.animate([{ opacity: .4 }, { opacity: 1 }], opacity),
    ];
    let cleaned = false;
    function cancel() {
      if (cleaned) return;
      cleaned = true;
      animations.forEach(animation => animation.cancel());
      viewport.remove();
    }
    const finished = Promise.allSettled(animations.map(animation => animation.finished)).then(cancel);
    function reverse() {
      if (!cleaned) animations.forEach(animation => animation.reverse());
    }
    return { finished, cancel, reverse };
  }

  window.AgentFlowSidebarMotion = { capture, play };
})();
