// Shared application context. In-document management views and React use the same store; page
// specific data and a conversation's persisted project remain in their owners.
(() => {
  let snapshot = Object.freeze({view: '', project: '', chatProject: '', session: '', issue: '', theme: 'light'});
  const events = new Map();
  window.loopforgeApplicationState = {
    getSnapshot: () => snapshot,
    update(patch) {
      if (Object.keys(patch).every(key => Object.is(snapshot[key], patch[key]))) return;
      snapshot = Object.freeze({...snapshot, ...patch});
    },
    on(type, listener) {
      let group = events.get(type);
      if (!group) events.set(type, group = new Set());
      group.add(listener);
      return () => { group.delete(listener); if (!group.size) events.delete(type); };
    },
    emit(type, ...values) {
      const field = {view: 'view', project: 'chatProject', session: 'session', theme: 'theme'}[type];
      if (field) this.update({[field]: values[0] || ''});
      for (const listener of events.get(type) || []) listener(...values);
    },
  };
})();
