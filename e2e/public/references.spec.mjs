import {test,expect} from '@playwright/test';
import {signInUnconfigured, switchProject, chat, openHome, fitsFrame} from './helpers.mjs';

// Real reference APIs, no model calls. Keep credentials out of trace artifacts.
test.use({trace:'off',screenshot:'off'});

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
      localStorage.setItem('loopforge.lang',lang);localStorage.setItem('agentflow.theme.v3',theme);
    },variant);
    await signInUnconfigured(page);
    await switchProject(page,'welcome');
    const input=await openHome(page);
    const frame=chat(page),picker=frame.locator('.mention-picker');
    const options=picker.locator('.mention-options [role="option"]');
    await input.fill('@');
    await expect(picker).toBeVisible();
    await expect.poll(()=>fitsFrame(picker)).toBe(true);
    await expect(options).toHaveCount(5);
    await page.screenshot({path:info.outputPath(`references-${variant.name}.png`)});
    const loading=page.waitForResponse(r=>{const u=new URL(r.url());return u.pathname==='/api/assistant/references'&&u.searchParams.get('type')==='sop'&&!u.searchParams.get('q')});
    await options.filter({hasText:'SOP'}).first().click();
    const loaded=await loading;expect(loaded.ok()).toBe(true);
    const data=await loaded.json(),items=Array.isArray(data)?data:data.items;
    expect(items.length).toBeGreaterThan(0);
    await expect(options.first().locator('strong')).toHaveText(items[0].name||items[0].id);
    await expect.poll(()=>fitsFrame(picker)).toBe(true);
    // Search a real SOP and select it; the test does not run the referenced SOP.
    const name=items[0].name||items[0].id;
    const searched=page.waitForResponse(r=>{const u=new URL(r.url());return u.pathname==='/api/assistant/references'&&u.searchParams.get('q')===name});
    await picker.locator('input[aria-label="搜索引用对象"]').fill(name);
    expect((await searched).ok()).toBe(true);
    await expect(options.first()).toContainText(name);
    await expect.poll(()=>fitsFrame(picker)).toBe(true);
    await options.first().click();
    await expect(picker).toBeHidden();
    await expect(input).toHaveValue(new RegExp('@'+name.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')+' '));
    await expect(frame.locator('.composer-attachments')).toContainText('@'+name);
    // A resize while the picker is open keeps it inside the frame.
    await input.fill(await input.inputValue()+'@');
    await expect(picker).toBeVisible();
    await page.setViewportSize({width:variant.width,height:Math.max(360,variant.height-160)});
    await expect.poll(()=>fitsFrame(picker)).toBe(true);
    await page.keyboard.press('Escape');
    await expect(picker).toBeHidden();
    await expect(input).toBeFocused();
    // Removing the reference chip also removes its text.
    await frame.getByRole('button',{name:`移除引用 ${name}`}).click();
    await expect(frame.locator('.composer-attachments')).toHaveCount(0);
    expect(errors).toEqual([]);
  });
}
