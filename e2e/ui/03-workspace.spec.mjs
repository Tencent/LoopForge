import {test,expect,watchErrors} from './fixtures.mjs';
import {get,cleanup,waitRun} from '../lib/api.mjs';
import {createWorkflow,script,approval,startRun} from '../lib/fixtures.mjs';

test.afterAll(cleanup);

test('bundled IDE: open a real workspace, load its file and render the editor',async({page})=>{
  const wf=await createWorkflow('ui-ide',[script('seed',"printf 'browser editor acceptance\\n' > note.txt"),approval('gate','seed')]);
  const {id}=await startRun(wf);
  await waitRun(id,r=>r.nodes?.gate?.status==='waiting_approval');
  const errors=watchErrors(page);
  await page.goto(`/vscode.html?run=${encodeURIComponent(id)}`);
  await expect(page.locator('.monaco-workbench')).toBeVisible({timeout:60_000});
  await expect(page.getByText('note.txt',{exact:true}).first()).toBeVisible({timeout:30_000});
  await page.getByText('note.txt',{exact:true}).first().dblclick();
  await expect(page.locator('.monaco-editor .view-lines')).toContainText('browser editor acceptance',{timeout:30_000});
  expect((await get(`/api/runs/${id}/workspace/file?path=note.txt`)).content).toBe('browser editor acceptance\n');
  expect(errors).toEqual([]);
});
