import {test,expect} from '@playwright/test';
import {signInUnconfigured} from './helpers.mjs';

test.use({trace:'off',screenshot:'off'});

test('GitLab credentials are independent, redacted and verified as identity only',async({page})=>{
  await signInUnconfigured(page);
  await expect(page.locator('#aiAssistant .ai-compose-input')).toBeEnabled();
  const current=await page.request.get('/api/auth/current').then(r=>r.json());
  const user=current.user;
  const credentials=`/api/users/${user.id}/credentials`;
  const inert='e2e-inert-gitlab-token-not-for-network-use';
  try{
    await page.goto('/#/settings');
    await page.locator('[name="gitLabBaseUrl"]').fill('https://gitlab.example.com');
    await page.locator('[name="gitLabToken"]').fill(inert);
    const pending=page.waitForResponse(r=>r.request().method()==='PUT'&&new URL(r.url()).pathname===credentials);
    await page.locator('#userCredentialForm button[type="submit"]').click();
    const saved=await pending;
    expect(saved.ok()).toBe(true);
    const publicUser=await saved.json();
    expect(publicUser.gitLabTokenConfigured).toBe(true);
    expect(publicUser.gitLabToken).toBeUndefined();
    expect(!!publicUser.gitTokenConfigured).toBe(!!user.gitTokenConfigured);
    expect(JSON.stringify(publicUser)).not.toContain(inert);
    await page.reload();
    await expect(page.locator('[name="gitLabToken"]')).toHaveValue('');
    await expect(page.locator('[name="gitLabBaseUrl"]')).toHaveValue('https://gitlab.example.com');
    await page.route('**/api/users/*/gitlab-token/verify',route=>route.fulfill({json:{valid:true,login:'gitlab-test-user'}}));
    await page.locator('#verifyGitLabToken').click();
    await expect(page.locator('#gitLabTokenVerifyStatus')).toContainText('gitlab-test-user');
    await expect(page.locator('#gitLabTokenVerifyStatus')).toContainText('仓库权限尚未验证');
    await page.locator('#langToggle').click();
    await expect(page.locator('#userCredentialForm')).toContainText('Self-managed GitLab');
  }finally{
    expect((await page.request.put(credentials,{data:{gitLabTokenClear:true,gitLabBaseUrl:''}})).ok()).toBe(true);
  }
});


test('project repository input accepts and saves a configured GitLab URL',async({page})=>{
  await signInUnconfigured(page);
  await expect(page.locator('#aiAssistant .ai-compose-input')).toBeEnabled();
  const {user}=await page.request.get('/api/auth/current').then(r=>r.json());
  expect(!!user.gitLabTokenConfigured).toBe(false);
  const credentials=`/api/users/${user.id}/credentials`;
  const projectId='e2e-gitlab-repository';
  let repositoryId;
  try{
    expect((await page.request.put(credentials,{data:{gitLabBaseUrl:'https://gitlab.example.com',gitLabToken:'e2e-inert-gitlab-token'}})).ok()).toBe(true);
    const existing=await page.request.get(`/api/projects/${projectId}`);
    if(existing.status()===404)expect((await page.request.post('/api/projects',{data:{id:projectId,key:'E2GL',name:'E2E GitLab repository',ownerId:user.id}})).ok()).toBe(true);
    await page.reload();
    await page.locator('#projectSwitcher').selectOption(projectId);
    await page.goto('/#/projects');
    await page.locator('#addRepoDraft').click();
    const input=page.locator('[data-r="repoUrl"]');
    await expect(input).toHaveAttribute('placeholder',/GitHub.*GitLab/);
    await input.fill('https://gitlab.example.com/team/sub/repository.git');
    await input.press('Tab');
    await expect(page.locator('[data-r="name"]')).toHaveValue('repository');
    const pending=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/repositories');
    await page.locator('[data-save-repo]').click();
    const response=await pending;expect(response.ok()).toBe(true);
    const saved=await response.json();repositoryId=saved.id;
    expect(saved.provider).toBe('gitlab');
    expect(saved.cloneUrl).toBe('https://gitlab.example.com/team/sub/repository.git');
    expect(saved.gongfengProjectId).toBe('gitlab:https://gitlab.example.com/team/sub/repository');
    await page.reload();
    await expect(page.locator(`a.repo-cell-link[href="${saved.webUrl}"]`).first()).toBeVisible();
  }finally{
    if(repositoryId)expect((await page.request.delete(`/api/repositories?id=${encodeURIComponent(repositoryId)}`)).ok()).toBe(true);
    expect((await page.request.put(credentials,{data:{gitLabTokenClear:true,gitLabBaseUrl:''}})).ok()).toBe(true);
  }
});
