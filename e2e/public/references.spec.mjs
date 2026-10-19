import {test,expect} from '@playwright/test';
import {signInUnconfigured} from './helpers.mjs';

// Real reference APIs, no model calls. Keep credentials out of trace artifacts.
test.use({trace:'off',screenshot:'off'});

async function signIn(page){
  await signInUnconfigured(page);
  await page.locator('#projectSwitcher').selectOption('welcome');
  await page.goto('/#/collaboration');
}

async function expectPickerInView(page){
  const picker=page.locator('.collab-reference-picker');
  await expect(picker).toBeVisible();
  await expect.poll(()=>picker.evaluate(el=>{
    const r=el.getBoundingClientRect(),host=document.querySelector('#aiAssistant').getBoundingClientRect();
    const view=window.visualViewport,top=view?.offsetTop||0,bottom=top+(view?.height||innerHeight);
    const header=document.querySelector('.topbar').getBoundingClientRect();
    const search=el.querySelector('input')?.getBoundingClientRect();
    return r.top>=Math.max(top,host.top,header.bottom)&&r.bottom<=Math.min(bottom,host.bottom)&&r.left>=0&&r.right<=innerWidth&&!!search&&search.height>0&&search.top>=r.top&&search.bottom<=r.bottom;
  })).toBe(true);
}

for(const variant of [
  {name:'desktop',width:1600,height:1000,lang:'zh',theme:'light'},
  {name:'laptop',width:1280,height:720,lang:'zh',theme:'light'},
  {name:'short-window',width:1280,height:400,lang:'zh',theme:'light'},
  {name:'mobile-English-dark',width:390,height:844,lang:'en',theme:'dark'},
]){
  test(`@ references stay visible, scroll and remain selectable: ${variant.name}`,async({page},info)=>{
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.setViewportSize({width:variant.width,height:variant.height});
    await page.addInitScript(({lang,theme})=>{
      localStorage.setItem('loopforge.lang',lang);localStorage.setItem('loopforge.theme.v3',theme);
    },variant);
    await signIn(page);
    const input=page.locator('#aiAssistant .ai-compose-input'),picker=page.locator('.collab-reference-picker');
    await input.fill('@');
    await expectPickerInView(page);
    await expect(picker.locator('.collab-reference-options [role="option"]')).toHaveCount(5);
    await page.screenshot({path:info.outputPath(`references-${variant.name}.png`)});
    // Scrolling the empty workbench must not leave the menu at stale coordinates.
    await page.evaluate(()=>document.querySelector('#aiAssistant').scrollBy(0,40));
    await expectPickerInView(page);
    const loading=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/assistant/references'&&new URL(r.url()).searchParams.get('type')==='sop'&&!new URL(r.url()).searchParams.get('q'));
    await picker.locator('.collab-reference-options [role="option"]').nth(2).click();
    const loaded=await loading;expect(loaded.ok()).toBe(true);
    const data=await loaded.json(),items=Array.isArray(data)?data:data.items;
    expect(items.length).toBeGreaterThan(0);
    const options=picker.locator('.collab-reference-options [role="option"]');
    await expect(options.first().locator('b')).toHaveText(items[0].name);
    await expectPickerInView(page);
    // Search a real SOP and select it; the test does not run the referenced SOP.
    const name=await options.first().locator('b').innerText();
    const searched=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/assistant/references'&&new URL(r.url()).searchParams.get('q')===name);
    await picker.locator('input').fill(name);
    expect((await searched).ok()).toBe(true);
    await expect(options.first()).toContainText(name);
    await expectPickerInView(page);
    await options.first().click();
    await expect(picker).toBeHidden();
    await expect(input).toHaveValue('@'+name+' ');
    await expect(page.locator('.collab-composer-context')).toContainText(name);
    // The toolbar entry and a viewport resize must use the same placement logic.
    await page.locator('.ai-compose-tools [data-node-context-tool]').nth(1).click();
    await expectPickerInView(page);
    await page.setViewportSize({width:variant.width,height:Math.max(360,variant.height-160)});
    await expectPickerInView(page);
    await picker.evaluate(el=>{el.scrollTop=el.scrollHeight});
    await expectPickerInView(page);
    await page.keyboard.press('Escape');
    await expect(picker).toBeHidden();
    await expect(input).toBeFocused();
    expect(errors).toEqual([]);
  });
}
