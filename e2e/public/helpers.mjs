import fs from 'node:fs';
export const password=()=>process.env.LOOPFORGE_TEST_PASSWORD || fs.readFileSync(process.env.LOOPFORGE_TEST_PASSWORD_FILE||new URL('../../.local/admin-password',import.meta.url),'utf8').trim();

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
    await expect(page.locator('#projectSwitcher')).toBeVisible();
    unconfiguredCookies=(await page.context().cookies()).filter(c=>c.name==='agentflow_user');
  }
  await expect(page.locator('#projectSwitcher')).toBeVisible();
}
