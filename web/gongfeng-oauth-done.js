(() => {
 // Only a completion signal crosses tabs: listeners reload the grant through authenticated APIs.
 try{if(typeof BroadcastChannel==='function'){const channel=new BroadcastChannel('agentflow-gongfeng-oauth');channel.postMessage({type:'gongfeng-oauth-completed'});channel.close();}}catch(_){}
 const showManual=()=>{document.getElementById('closing').hidden=true;document.getElementById('manual').hidden=false;};
 document.getElementById('closePage').onclick=()=>{window.close();setTimeout(showManual,300);};
 // Browsers may refuse to close a tab the script did not open; then the page stays as a hint.
 try{window.close();}catch(_){}
 setTimeout(showManual,500);
})();
