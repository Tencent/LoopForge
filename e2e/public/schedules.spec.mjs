import {test,expect} from '@playwright/test';
import {password,appReady,switchProject} from './helpers.mjs';

test.use({trace:'off',screenshot:'off'});

for(const width of [1280,390]){
  test(`AI schedule reasoning effort persists at ${width}px`,async({page})=>{
    await page.setViewportSize({width,height:800});
    await page.goto('/login.html');
    await page.locator('[name="login"]').fill(process.env.LOOPFORGE_TEST_USER||'admin');
    await page.locator('[name="password"]').fill(password());
    await page.locator('#loginSubmit').click();
    await appReady(page);
    const current=await page.request.get('/api/auth/current');
    expect(current.ok()).toBe(true);
    const {user}=await current.json();
    const credentialPath=`/api/users/${user.id}/credentials`;
    const fixtureKey=!user.codeBuddyApiKeyConfigured;
    let id;
    try{
      if(fixtureKey){
        const credential=await page.request.put(credentialPath,{data:{codeBuddyApiKey:'e2e-schedule-configuration-only'}});
        expect(credential.ok(),await credential.text()).toBe(true);
      }
      // A disabled job exercises configuration without making a model request.
      const created=await page.request.post('/api/schedule-jobs',{data:{name:`Effort acceptance ${width}`,projectId:'welcome',skipDispatch:true,assistantPrompt:'Inspect the project.',intervalSec:3600,enabled:false}});
      expect(created.ok(),await created.text()).toBe(true);
      id=(await created.json()).id;
      await switchProject(page,'welcome');
      const open=async()=>{
        await page.goto('/#/aiSchedules');
        await expect(page.locator('#newAISchedule')).toBeVisible();
        if(width===390&&await page.locator('#settingsSidebarToggle[aria-expanded="true"]').isVisible())await page.locator('#settingsSidebarToggle').click();
        await page.locator(`#aiScheduleJobList button.schedule-job-main[data-edit-schedule="${id}"]`).click();
      };
      await open();
      const form=page.locator('#scheduleJobForm');
      const effort=form.locator('[name="assistantEffort"]');
      await expect(effort.locator('option[value="high"]')).toBeAttached();
      await expect(effort).toBeVisible();
      await effort.selectOption('high');
      if(width===390){
        const columns=await form.locator('.schedule-model-row').evaluate(el=>getComputedStyle(el).gridTemplateColumns);
        expect(columns.trim().split(/\s+/)).toHaveLength(1);
      }
      const pending=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/schedule-jobs'&&response.request().method()==='PUT');
      await form.locator('[type="submit"]').click();
      const response=await pending;
      expect(response.ok(),await response.text()).toBe(true);
      const saved=await response.json();
      expect(saved.assistantEffort).toBe('high');
      expect(saved.enabled).toBe(false);
      await page.reload();
      await appReady(page);
      await switchProject(page,'welcome');
      await open();
      await expect(effort).toHaveValue('high');
      const jobs=await page.request.get('/api/schedule-jobs');
      expect((await jobs.json()).find(job=>job.id===id).assistantEffort).toBe('high');
    }finally{
      if(id)await page.request.delete(`/api/schedule-jobs/${encodeURIComponent(id)}`);
      if(fixtureKey){
        const credential=await page.request.put(credentialPath,{data:{codeBuddyApiKey:''}});
        expect(credential.ok(),await credential.text()).toBe(true);
      }
    }
  });
}
