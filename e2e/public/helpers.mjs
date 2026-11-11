import fs from 'node:fs';
export const password=()=>process.env.LOOPFORGE_TEST_PASSWORD || fs.readFileSync(process.env.LOOPFORGE_TEST_PASSWORD_FILE||new URL('../../.local/admin-password',import.meta.url),'utf8').trim();

// The global #projectSwitcher <select> owns the active project, but it is
// drawn as a Harness trigger and the whole top bar is hidden on the chat home,
// so readiness means "projects loaded", not "select visible".
// A page whose console never booted is reported (scripts still loading, page
// errors) and reloaded once, so a stalled load on a busy runner is diagnosable
// without failing the journey that follows.
export async function appReady(page){
  const {expect}=await import('@playwright/test');
  const option=page.locator('#projectSwitcher option').first();
  try{
    await expect(option).toBeAttached({timeout:20_000});
    return;
  }catch(err){
    const state=await page.evaluate(()=>{
      const loaded=new Set(performance.getEntriesByType('resource').filter(e=>e.responseEnd>0).map(e=>e.name));
      return {url:location.pathname+location.hash,readyState:document.readyState,
        pendingScripts:[...document.scripts].filter(s=>s.src&&!loaded.has(s.src)).map(s=>new URL(s.src).pathname)};
    }).catch(e=>({error:e.message}));
    console.warn(`[appReady] console did not boot, reloading once: ${JSON.stringify(state)}`);
  }
  await page.reload();
  await expect(option).toBeAttached({timeout:20_000});
}
export async function switchProject(page,id){
  const {expect}=await import('@playwright/test');
  await expect(page.locator(`#projectSwitcher option[value="${id}"]`)).toHaveCount(1);
  await page.locator('#projectSwitcher').selectOption(id,{force:true});
}

// Flow templates and runs sit in the settings sidebar, opened from the rail's
// settings entry; the pinned release that first-run installs still shows them
// on the rail, so click whichever entry is visible.
export async function openSettingsPage(page,view){
  const {expect}=await import('@playwright/test');
  const entry=page.locator(`.nav[data-view="${view}"]:visible`).first();
  if(!(await entry.count()))await page.locator('#railSettingsEntry').click();
  await expect(entry).toBeVisible();
  await entry.click();
}

// The home page embeds the chat app in an iframe; its composer is the entry point.
export const chat=page=>page.frameLocator('iframe[src*="/chatbot/"]');
export const composer=page=>chat(page).locator('.composer-column textarea');
export async function openHome(page){
  const {expect}=await import('@playwright/test');
  await page.goto('/#/collaboration');
  await expect(composer(page)).toBeEditable();
  // The saved draft is restored once the signed-in user loads; typing earlier is overwritten.
  await page.waitForLoadState('networkidle');
  return composer(page);
}
// A menu or dialog inside the chat frame must fit the frame's viewport.
export const fitsFrame=locator=>locator.evaluate(el=>{
  const r=el.getBoundingClientRect();
  return r.width>0&&r.top>=0&&r.left>=0&&r.bottom<=innerHeight+1&&r.right<=innerWidth+1;
});

// Reuse a real password session within this worker so layout/API journeys do
// not exhaust the production login rate limit. Cookies stay only in memory.
let unconfiguredCookies=[];
export async function signInUnconfigured(page){
  const {test,expect}=await import('@playwright/test');
  test.skip(!process.env.LOOPFORGE_UNCONFIGURED_USER,'Provide an account without model or Git credentials');
  if(unconfiguredCookies.length){
    await page.context().addCookies(unconfiguredCookies);
    await page.goto('/#/collaboration');
  }else{
    const secret=process.env.LOOPFORGE_UNCONFIGURED_PASSWORD||fs.readFileSync(process.env.LOOPFORGE_UNCONFIGURED_PASSWORD_FILE,'utf8').trim();
    await page.goto('/login.html');
    await page.locator('[name="login"]').fill(process.env.LOOPFORGE_UNCONFIGURED_USER);
    await page.locator('[name="password"]').fill(secret);
    const pending=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/auth/password'&&r.request().method()==='POST');
    await page.locator('#loginSubmit').click();
    const response=await pending;
    if(!response.ok()){
      // Playwright's error-context snapshot can include password input values.
      await page.locator('[name="password"]').fill('');
      throw new Error(`Acceptance account login failed: HTTP ${response.status()}`);
    }
    await appReady(page);
    unconfiguredCookies=(await page.context().cookies()).filter(c=>c.name==='agentflow_user');
  }
  await appReady(page);
}
