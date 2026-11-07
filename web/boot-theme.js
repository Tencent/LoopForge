// Loaded synchronously from <head> (CSP forbids inline scripts) so the first paint already has the right theme.
// Read-only mirror of readThemePreference/resolvedTheme in app.js; app.js still migrates and clears the legacy key.
(function(){
  var theme='light';
  try{
    var valid=function(value){return value==='light'||value==='dark'||value==='system'};
    var preference=localStorage.getItem('agentflow.theme.v3');
    if(!valid(preference))preference=localStorage.getItem('agentflow.native-chat.theme');
    if(!valid(preference))preference=localStorage.getItem('loopforge.theme.v3');
    if(!valid(preference))preference='light';
    theme=preference==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):preference;
  }catch(e){theme='light'}
  document.documentElement.dataset.theme=theme;
})();
