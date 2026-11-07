// Pan/zoom viewport for the flow designer canvas. Node positions stay in
// logical canvas coordinates; only #canvasStage is transformed.
const flowViewport=(()=>{
  const MIN=.25,MAX=2,GRID=18,NODE_W=244,NODE_H=132,PAL_W=256,PAL_MINI=48,MINIMAP_KEY='agentflow.flowMinimap';
  const vp={x:40,y:40,zoom:1};
  let fittedId=null,autoCollapsed=false,animTimer=0,bound=false,onBlankClick=null,minimapRaf=0;
  const byId=id=>document.getElementById(id);
  const canvas=()=>byId('canvas');
  const clamp=z=>Math.min(MAX,Math.max(MIN,z));

  function apply(anim){
    const stage=byId('canvasStage'),c=canvas();if(!stage||!c)return;
    stage.classList.toggle('anim',!!anim);
    stage.style.transform=`translate(${vp.x}px,${vp.y}px) scale(${vp.zoom})`;
    const g=GRID*vp.zoom;c.style.backgroundSize=`${g}px ${g}px`;c.style.backgroundPosition=`${vp.x}px ${vp.y}px`;
    const zv=byId('canvasZoomValue');if(zv)zv.textContent=Math.round(vp.zoom*100)+'%';
    scheduleMinimap();
    clearTimeout(animTimer);
    if(anim)animTimer=setTimeout(()=>stage.classList.remove('anim'),400);
  }
  function rect(){return canvas()?.getBoundingClientRect()||{left:0,top:0,width:0,height:0}}
  function toWorld(clientX,clientY){const r=rect();return {x:(clientX-r.left-vp.x)/vp.zoom,y:(clientY-r.top-vp.y)/vp.zoom}}
  function drawerWidth(){
    const wrap=byId('flowStageWrap');if(!wrap||!wrap.classList.contains('has-drawer'))return 0;
    const open=wrap.querySelector('.fd-drawer.open,.fd-ai-panel:not([hidden])');
    return open?open.offsetWidth:368;
  }
  // Visible canvas size, minus the drawer that overlays its right edge.
  function visible(){const c=canvas();return {w:Math.max(0,(c?.clientWidth||0)-drawerWidth()),h:c?.clientHeight||0}}
  function zoomAt(z,clientX,clientY,anim){
    const r=rect(),v=visible();
    const cx=clientX??r.left+v.w/2,cy=clientY??r.top+v.h/2;
    const w=toWorld(cx,cy);
    vp.zoom=clamp(z);vp.x=cx-r.left-w.x*vp.zoom;vp.y=cy-r.top-w.y*vp.zoom;
    apply(anim);
  }
  function zoomBy(factor,clientX,clientY){zoomAt(vp.zoom*factor,clientX,clientY,clientX==null)}
  function setZoom(z){zoomAt(z,null,null,true)}
  function nodeSize(id){
    const m=typeof flowNodeMetrics==='function'?flowNodeMetrics(id):null;
    if(m)return m;
    const el=document.querySelector(`#nodes .flow-node[data-id="${CSS.escape(id)}"]`);
    return {w:el?.offsetWidth||NODE_W,h:el?.offsetHeight||NODE_H};
  }
  function bounds(){
    const nodes=state.workflow?.nodes||[];
    if(!nodes.length)return null;
    let x0=Infinity,y0=Infinity,x1=-Infinity,y1=-Infinity;
    nodes.forEach(n=>{const p=n.position||{x:0,y:0},s=nodeSize(n.id);x0=Math.min(x0,p.x);y0=Math.min(y0,p.y);x1=Math.max(x1,p.x+s.w);y1=Math.max(y1,p.y+s.h+(n.rejectTo?60:0))});
    return {x:x0,y:y0,w:x1-x0,h:y1-y0};
  }
  function fit(opts={}){
    const smooth=opts.smooth!==false,pad=opts.padding??72,v=visible();
    if(!v.w||!v.h)return;
    const b=bounds();
    if(!b){vp.zoom=1;vp.x=40;vp.y=40;apply(smooth);return}
    vp.zoom=clamp(Math.min(opts.maxZoom??1,(v.w-pad*2)/b.w,(v.h-pad*2)/b.h));
    vp.x=(v.w-b.w*vp.zoom)/2-b.x*vp.zoom;
    vp.y=(v.h-b.h*vp.zoom)/2-b.y*vp.zoom;
    apply(smooth);
  }
  function centerOn(x,y,opts={}){
    const v=visible();if(!v.w)return;
    vp.x=v.w/2-x*vp.zoom;vp.y=v.h/2-y*vp.zoom;
    apply(!!opts.smooth);
  }
  function reset(opts={}){vp.zoom=1;vp.x=40;vp.y=40;apply(!!opts.smooth)}
  // Pans the least distance that brings a node fully into the visible area.
  function reveal(nodeId){
    const n=(state.workflow?.nodes||[]).find(x=>x.id===nodeId);if(!n)return;
    const v=visible(),s=nodeSize(nodeId),m=24,top=64,bottom=72;
    const sx=n.position.x*vp.zoom+vp.x,sy=n.position.y*vp.zoom+vp.y,sw=s.w*vp.zoom,sh=s.h*vp.zoom;
    let dx=0,dy=0;
    if(sx+sw>v.w-m)dx=v.w-m-(sx+sw);
    if(sx+dx<m)dx=m-sx;
    if(sy+sh>v.h-bottom)dy=v.h-bottom-(sy+sh);
    if(sy+dy<top)dy=top-sy;
    if(dx||dy){vp.x+=dx;vp.y+=dy;apply(true)}
  }
  // A world point inside the visible area that does not sit on another node.
  function freeSpot(){
    const v=visible(),nodes=state.workflow?.nodes||[];
    const c={x:(v.w/2-vp.x)/vp.zoom-NODE_W/2,y:(v.h/2-vp.y)/vp.zoom-NODE_H/2};
    for(let i=0;i<24;i++){
      const p={x:Math.round((c.x+(i%6)*36)/10)*10,y:Math.round((c.y+Math.floor(i/6)*48)/10)*10};
      if(!nodes.some(n=>Math.abs(n.position.x-p.x)<60&&Math.abs(n.position.y-p.y)<60))return p;
    }
    return {x:Math.round(c.x/10)*10,y:Math.round(c.y/10)*10};
  }
  // Collapse the palette and hide minimap/legend when an open drawer leaves
  // too little canvas; restore whatever was collapsed automatically.
  function layoutTight(){
    const body=byId('flowBody'),pal=byId('flowPalette'),wrap=byId('flowStageWrap');if(!body||!pal||!wrap)return;
    const drawer=drawerWidth(),total=body.clientWidth;if(!total)return;
    const manual=localStorage.getItem('agentflow.flowPaletteCollapsed')==='1';
    const need=drawer>0&&total-(manual?PAL_MINI:PAL_W)-drawer<480;
    if(need&&!pal.classList.contains('collapsed')){pal.classList.add('collapsed');autoCollapsed=true}
    else if(!need&&autoCollapsed){pal.classList.remove('collapsed');autoCollapsed=false}
    const free=total-(pal.classList.contains('collapsed')?PAL_MINI:PAL_W)-drawer;
    wrap.classList.toggle('tight',free<640);
  }
  function paletteToggled(){autoCollapsed=false}
  function minimapOn(){return localStorage.getItem(MINIMAP_KEY)!=='0'}
  // Pan and wheel fire faster than frames; redraw the minimap at most once per frame.
  function scheduleMinimap(){
    if(minimapRaf)return;
    minimapRaf=requestAnimationFrame(()=>{minimapRaf=0;renderMinimap()});
  }
  function renderMinimap(){
    const box=byId('canvasMinimap'),svg=byId('canvasMinimapSvg');if(!box||!svg)return;
    box.hidden=!minimapOn();byId('canvasMinimapToggle')?.classList.toggle('on',minimapOn());
    if(box.hidden||!box.offsetWidth)return;
    const nodes=state.workflow?.nodes||[],v=visible(),W=box.clientWidth,H=box.clientHeight;
    const view={x:-vp.x/vp.zoom,y:-vp.y/vp.zoom,w:v.w/vp.zoom,h:v.h/vp.zoom};
    const b=bounds()||{x:view.x,y:view.y,w:view.w,h:view.h};
    const x0=Math.min(b.x,view.x)-40,y0=Math.min(b.y,view.y)-40,x1=Math.max(b.x+b.w,view.x+view.w)+40,y1=Math.max(b.y+b.h,view.y+view.h)+40;
    const s=Math.min(W/(x1-x0),H/(y1-y0)),ox=(W-(x1-x0)*s)/2,oy=(H-(y1-y0)*s)/2;
    box._map={x0,y0,s,ox,oy};
    svg.innerHTML=nodes.map(n=>{const z=nodeSize(n.id),tint=typeof flowNodeTint==='function'?flowNodeTint(n):'79 107 255';return `<rect class="fd-mm-node" x="${(ox+(n.position.x-x0)*s).toFixed(1)}" y="${(oy+(n.position.y-y0)*s).toFixed(1)}" width="${(z.w*s).toFixed(1)}" height="${(z.h*s).toFixed(1)}" rx="2.5" style="fill:rgb(${tint} / .55)"/>`}).join('')
      +`<rect class="fd-mm-view" x="${(ox+(view.x-x0)*s).toFixed(1)}" y="${(oy+(view.y-y0)*s).toFixed(1)}" width="${(view.w*s).toFixed(1)}" height="${(view.h*s).toFixed(1)}" rx="4"/>`;
  }
  function toggleMinimap(){localStorage.setItem(MINIMAP_KEY,minimapOn()?'0':'1');renderMinimap()}
  // Called after every canvas render; fits once per opened workflow.
  function afterRender(){
    const id=state.workflow?.id||null,c=canvas();
    if(id&&id!==fittedId&&c&&c.clientWidth){fittedId=id;requestAnimationFrame(()=>fit({smooth:false}))}
    else renderMinimap();
  }
  function bind(opts={}){
    onBlankClick=opts.onBlankClick||onBlankClick;
    const c=canvas();if(bound||!c)return;bound=true;
    c.addEventListener('wheel',e=>{
      e.preventDefault();
      const wheelLike=e.deltaMode===1||(!e.deltaX&&Math.abs(e.deltaY)>=40&&Number.isInteger(e.deltaY));
      if(e.ctrlKey||e.metaKey||wheelLike)zoomAt(vp.zoom*Math.exp(-e.deltaY*(e.deltaMode===1?.05:.0022)),e.clientX,e.clientY);
      else{vp.x-=e.deltaX;vp.y-=e.deltaY;apply()}
    },{passive:false});
    c.addEventListener('pointerdown',e=>{
      if(e.button!==0&&e.button!==1)return;
      if(e.target.closest('.flow-node,.fd-menu,.fd-e,button,input,textarea,select,a'))return;
      const sx=e.clientX,sy=e.clientY,ox=vp.x,oy=vp.y;let moved=false;
      c.setPointerCapture(e.pointerId);
      const move=ev=>{if(!moved&&Math.hypot(ev.clientX-sx,ev.clientY-sy)<3)return;moved=true;c.classList.add('panning');vp.x=ox+ev.clientX-sx;vp.y=oy+ev.clientY-sy;apply()};
      const up=()=>{c.removeEventListener('pointermove',move);c.removeEventListener('pointerup',up);c.removeEventListener('pointercancel',up);c.classList.remove('panning');if(!moved&&onBlankClick)onBlankClick(e)};
      c.addEventListener('pointermove',move);c.addEventListener('pointerup',up);c.addEventListener('pointercancel',up);
    });
    const on=(id,fn)=>{const el=byId(id);if(el)el.onclick=fn};
    on('canvasZoomIn',()=>zoomBy(1.2));
    on('canvasZoomOut',()=>zoomBy(1/1.2));
    on('canvasZoomValue',()=>setZoom(1));
    on('canvasFit',()=>fit());
    on('canvasMinimapToggle',toggleMinimap);
    const mm=byId('canvasMinimap');
    if(mm){
      let down=false;
      const go=e=>{const M=mm._map;if(!M)return;const r=mm.getBoundingClientRect();centerOn((e.clientX-r.left-M.ox)/M.s+M.x0,(e.clientY-r.top-M.oy)/M.s+M.y0)};
      mm.onpointerdown=e=>{e.stopPropagation();down=true;mm.setPointerCapture(e.pointerId);go(e)};
      mm.onpointermove=e=>{if(down)go(e)};
      mm.onpointerup=mm.onpointercancel=()=>{down=false};
    }
    const body=byId('flowBody');
    if(body&&typeof ResizeObserver==='function')new ResizeObserver(()=>{layoutTight();renderMinimap()}).observe(body);
    apply();
  }
  return {toWorld,zoomBy,setZoom,fit,centerOn,reveal,reset,apply,layoutTight,freeSpot,afterRender,bind,paletteToggled,renderMinimap,
    get zoom(){return vp.zoom},get x(){return vp.x},get y(){return vp.y},
    _set(next){Object.assign(vp,next)}};
})();
