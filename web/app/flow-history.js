// Undo/redo for the flow designer. Snapshots hold only the graph
// (nodes/edges/preview); workflow metadata edits are not part of the history.
const flowHistory=(()=>{
  const LIMIT=50,MERGE_MS=500;
  let undoStack=[],redoStack=[],workflowId=null,lastKey='',lastAt=0,version=0;
  const snap=wf=>JSON.stringify({nodes:wf.nodes||[],edges:wf.edges||[],preview:wf.preview===undefined?null:wf.preview});
  function bound(){
    const id=state.workflow?.id||null;
    if(id!==workflowId){undoStack=[];redoStack=[];workflowId=id;lastKey='';lastAt=0;version++}
    return !!state.workflow;
  }
  function restore(raw){
    const wf=state.workflow,s=JSON.parse(raw);
    wf.nodes=s.nodes;wf.edges=s.edges;
    if(s.preview==null)delete wf.preview;else wf.preview=s.preview;
    if(state.selectedNode&&!wf.nodes.some(n=>n.id===state.selectedNode))state.selectedNode=null;
    state.connectFrom=null;state.connectCursor=null;
    renderCanvas();renderProperties();dirty();sync();
  }
  // Call before mutating. Records sharing a merge key within 500ms collapse
  // into one entry so a burst of keystrokes undoes as a single change.
  function record(opts={}){
    if(!bound())return;
    const now=Date.now(),key=opts.merge||'';
    if(key&&key===lastKey&&now-lastAt<MERGE_MS){lastAt=now;return}
    undoStack.push(snap(state.workflow));
    if(undoStack.length>LIMIT)undoStack.shift();
    redoStack=[];lastKey=key;lastAt=now;version++;
    sync();
  }
  function undo(){
    if(!bound()||!undoStack.length)return false;
    redoStack.push(snap(state.workflow));
    version++;restore(undoStack.pop());lastKey='';
    return true;
  }
  function redo(){
    if(!bound()||!redoStack.length)return false;
    undoStack.push(snap(state.workflow));
    version++;restore(redoStack.pop());lastKey='';
    return true;
  }
  function clear(){undoStack=[];redoStack=[];workflowId=state.workflow?.id||null;lastKey='';lastAt=0;version++;sync()}
  function canUndo(){bound();return undoStack.length>0}
  function canRedo(){bound();return redoStack.length>0}
  function sync(){
    if(typeof document==='undefined')return;
    const u=document.getElementById('canvasUndo'),r=document.getElementById('canvasRedo');
    if(u)u.disabled=!canUndo();
    if(r)r.disabled=!canRedo();
  }
  // Bumps on every history change; lets a toast's undo detect later edits.
  function current(){bound();return version}
  return {record,undo,redo,clear,canUndo,canRedo,sync,current,limit:LIMIT};
})();
