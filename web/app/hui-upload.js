/* Harness upload zone for visible <input type="file">. The input keeps its
 * files, accept/multiple/webkitdirectory and change event; this file draws a
 * dashed drop zone with a "选择文件" button and the chosen file list after it.
 * Hidden inputs opened by a custom button (and [data-native]) are left alone.
 */
(function () {
  'use strict';
  if (typeof window === 'undefined') return;
  const doc = typeof document !== 'undefined' ? document : null;
  const supported = !!(doc && typeof doc.createElement === 'function' && typeof MutationObserver === 'function');
  const instances = new WeakMap();
  const LIST_LIMIT = 5;

  function el(tag, className, text) {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }
  function icon(name) {
    const svg = doc.createElementNS ? doc.createElementNS('http://www.w3.org/2000/svg', 'svg') : doc.createElement('svg');
    svg.setAttribute('class', 'agent-icon harness-icon');
    svg.setAttribute('data-icon', name);
    svg.setAttribute('width', '14');
    svg.setAttribute('height', '14');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    return svg;
  }
  function descriptor(target, name) {
    for (let proto = Object.getPrototypeOf(target); proto; proto = Object.getPrototypeOf(proto)) {
      const found = Object.getOwnPropertyDescriptor(proto, name);
      if (found) return found;
    }
    return null;
  }
  function size(bytes) {
    const n = Number(bytes) || 0;
    if (n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(1) + ' KB';
    return n + ' B';
  }
  // accept="…" semantics: extensions (.json), exact MIME types and type/*.
  function accepts(accept, file) {
    const rules = String(accept || '').split(',').map(rule => rule.trim().toLowerCase()).filter(Boolean);
    if (!rules.length) return true;
    const name = String(file?.name || '').toLowerCase();
    const type = String(file?.type || '').toLowerCase();
    return rules.some(rule => {
      if (rule.startsWith('.')) return name.endsWith(rule);
      if (rule.endsWith('/*')) return type.startsWith(rule.slice(0, -1));
      return type === rule;
    });
  }
  function hiddenInput(input) {
    if (input.hidden || input.style?.display === 'none') return true;
    if (typeof getComputedStyle !== 'function' || !input.isConnected) return false;
    try { return getComputedStyle(input).display === 'none'; } catch { return false; }
  }
  // Label text without nested controls (a wrapping <label> also contains the
  // select's options or the Harness trigger).
  function ownText(label) {
    const walk = node => {
      if (node.nodeType === 3) return node.textContent;
      if (node.nodeType !== 1 || /^(select|button|textarea|input|svg)$/i.test(node.tagName)) return '';
      return [...node.childNodes].map(walk).join('');
    };
    return label ? walk(label).replace(/\s+/g, ' ').trim() : '';
  }
  function isFileInput(node) {
    return !!node && node.tagName === 'INPUT' && String(node.getAttribute?.('type') || node.type || '').toLowerCase() === 'file';
  }

  function enhance(input) {
    if (!supported || !isFileInput(input)) return null;
    if (instances.has(input)) return instances.get(input);
    if (input.closest?.('[data-native]') || hiddenInput(input)) return null;
    const folder = !!(input.webkitdirectory || input.hasAttribute?.('webkitdirectory'));

    const zone = el('div', 'hui-upload');
    zone.appendChild(icon('paperclip'));
    const line = el('div', 'hui-upload-line');
    line.appendChild(doc.createTextNode ? doc.createTextNode(folder ? '选择要上传的文件夹 ' : '拖拽文件到此处，或 ') : el('span', null, folder ? '选择要上传的文件夹 ' : '拖拽文件到此处，或 '));
    const pick = el('button', 'hui-upload-pick', folder ? '选择文件夹' : '选择文件');
    pick.type = 'button';
    const own = input.getAttribute('aria-label') || ownText(input.labels?.[0]);
    if (own) pick.setAttribute('aria-label', own + '：' + pick.textContent);
    line.appendChild(pick);
    zone.appendChild(line);
    const hintText = [input.accept ? '支持 ' + input.accept.split(',').map(s => s.trim()).filter(Boolean).join(' / ') : '', input.multiple || folder ? '' : '单个文件'].filter(Boolean).join('，');
    const hint = el('small', 'hui-upload-hint', hintText);
    hint.hidden = !hintText;
    const error = el('small', 'hui-upload-error');
    error.setAttribute('role', 'alert');
    error.hidden = true;
    const list = el('ul', 'hui-upload-files');
    list.hidden = true;
    zone.appendChild(hint);
    zone.appendChild(error);
    zone.appendChild(list);

    input.classList.add('hui-upload-native');
    input.setAttribute('aria-hidden', 'true');
    const tabIndex = input.tabIndex;
    input.tabIndex = -1;
    input.after(zone);

    function fail(message) {
      error.textContent = message;
      error.hidden = !message;
    }
    function render() {
      const files = [...(input.files || [])];
      const items = files.slice(0, LIST_LIMIT).map(file => {
        const item = el('li');
        item.appendChild(icon('doc'));
        item.appendChild(el('span', null, file.webkitRelativePath || file.name));
        item.appendChild(el('small', null, size(file.size)));
        return item;
      });
      if (files.length > LIST_LIMIT) items.push(el('li', 'hui-upload-more', '等 ' + files.length + ' 个文件'));
      list.replaceChildren(...items);
      list.hidden = !files.length;
      const disabled = !!input.disabled;
      pick.disabled = disabled;
      zone.classList.toggle('is-disabled', disabled);
      zone.hidden = !!input.hidden || input.style?.display === 'none';
      window.AgentFlowHarnessIcons?.hydrate?.(zone);
    }
    function take(files) {
      if (input.disabled) return false;
      if (folder) { fail('文件夹请点击「选择文件夹」上传'); return false; }
      if (!files.length) return false;
      if (!input.multiple && files.length > 1) { fail('只能选择一个文件'); return false; }
      const rejected = files.filter(file => !accepts(input.accept, file));
      if (rejected.length) { fail('不支持的文件类型：' + rejected.map(file => file.name).join('、')); return false; }
      if (typeof DataTransfer !== 'function') { fail('当前浏览器不支持拖放，请点击选择'); return false; }
      const transfer = new DataTransfer();
      files.forEach(file => transfer.items.add(file));
      input.files = transfer.files;
      fail('');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
      render();
      return true;
    }

    pick.addEventListener('click', () => { if (!input.disabled) input.click(); });
    const over = event => {
      if (input.disabled) return;
      event.preventDefault();
      zone.classList.add('is-dragover');
    };
    zone.addEventListener('dragenter', over);
    zone.addEventListener('dragover', over);
    zone.addEventListener('dragleave', event => {
      if (!zone.contains(event.relatedTarget)) zone.classList.remove('is-dragover');
    });
    zone.addEventListener('drop', event => {
      event.preventDefault();
      zone.classList.remove('is-dragover');
      take([...(event.dataTransfer?.files || [])]);
    });
    const onChange = () => { fail(''); render(); };
    input.addEventListener('change', onChange);
    const form = input.form;
    const onReset = () => { fail(''); (typeof setTimeout === 'function' ? setTimeout : fn => fn())(render, 0); };
    form?.addEventListener('reset', onReset);
    const base = descriptor(input, 'value');
    if (base?.set) {
      Object.defineProperty(input, 'value', {
        configurable: true,
        enumerable: base.enumerable,
        get() { return base.get.call(this); },
        set(next) { base.set.call(this, next); render(); },
      });
    }
    const observer = new MutationObserver(render);
    observer.observe(input, { attributes: true });

    const api = {
      node: input,
      input,
      zone,
      take,
      refresh: render,
      destroy() {
        if (!instances.has(input)) return;
        observer.disconnect();
        input.removeEventListener?.('change', onChange);
        form?.removeEventListener?.('reset', onReset);
        delete input.value;
        input.classList.remove('hui-upload-native');
        input.removeAttribute?.('aria-hidden');
        input.tabIndex = tabIndex;
        zone.remove?.();
        instances.delete(input);
        window.HuiAuto?.untrack(api);
      },
    };
    instances.set(input, api);
    window.HuiAuto?.track(api);
    render();
    return api;
  }

  window.HuiUpload = Object.freeze({ enhance, accepts, destroy(input) { instances.get(input)?.destroy(); } });
  window.HuiAuto?.auto('input[type="file"]', enhance);
})();
