import selectorParser from 'postcss-selector-parser';

// Restore the style boundary previously provided by the iframe. Portals are
// mounted inside #chatRoot so both component and overlay rules stay scoped.
export default function scopeChatCSS() {
  return {
    postcssPlugin: 'scope-chat-css',
    Rule(rule) {
      if (rule.parent?.type === 'atrule' && /keyframes$/i.test(rule.parent.name)) return;
      const selectors = selectorParser().astSync(rule.selector);
      rule.selector = selectors.nodes.map((selector) => {
        let value = selector.toString().trim();
        // Theme selectors refer to the host document, not a descendant of the
        // workbench. Their declarations may also define workbench variables.
        const themed = value.match(/^(?:html|:root)?(\[data-theme[^\]]*\])(?:\s+|$)/);
        if (themed) {
          value = value.slice(themed[0].length).trim();
          return `${themed[1]} #chatRoot${value ? ' ' + value : ''}`;
        }
        // Standalone document roots now mean the workbench container.
        value = value.replace(/^(?::root|html|body|#root)(?=$|[\s.:#\[])/, '').trim();
        return `#chatRoot${value ? ' ' + value : ''}`;
      }).join(', ');
    },
  };
}
