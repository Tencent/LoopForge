// A complete software change through real model roles and an actual human gate.
import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {api,post,cleanup,ensureProject,waitRun,nodeOf} from '../lib/api.mjs';
import {cfg} from '../lib/env.mjs';
import {createAgent,createWorkflow,agentNode,script,approval,startRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('real delivery SOP plans, fixes code, reviews, tests, waits for approval and delivers',{skip:!cfg.model,timeout:900_000},async()=>{
  const common='这是隔离项目内的真实自动化验收。只处理当前工作目录的测试任务，不访问网络、不安装依赖、不执行 Git、不修改测试文件。';
  const plan=await createAgent('delivery-plan',common+' 阅读 mathutil.py 和 test_mathutil.py，写 plan.md，简要说明 clamp 的目标与实现计划。不修改代码。');
  const implement=await createAgent('delivery-implement',common+' 阅读 plan.md，修复 mathutil.py 的 clamp，使已有测试通过。执行 python3 -m unittest test_mathutil。写 implementation.md 记录实际改动和测试。');
  const review=await createAgent('delivery-review',common+' 独立检查 mathutil.py 和 test_mathutil.py，并执行 python3 -m unittest test_mathutil。不要修改代码。将真实评审结论写 review.md，通过时第一行写 APPROVED，有缺陷时写 REJECT 并说明。');
  const deliver=await createAgent('delivery-summary',common+' 阅读 plan.md、implementation.md、review.md 和 verification.txt，将已完成的变更、测试结果与交付物清单写 delivery.md。只写总结，不修改代码或已有报告。');
  const artifact=(name,path,mediaType='text/markdown')=>({name,path,mediaType,required:true});
  const fixture=[
    "cat > mathutil.py <<'PY'",'def clamp(value, lower, upper):','    return value','PY',
    "cat > test_mathutil.py <<'PY'",'import unittest','from mathutil import clamp','class ClampTests(unittest.TestCase):',
    '    def test_lower(self): self.assertEqual(0, clamp(-2, 0, 10))',
    '    def test_inside(self): self.assertEqual(4, clamp(4, 0, 10))',
    '    def test_upper(self): self.assertEqual(10, clamp(12, 0, 10))','PY',
  ].join('\n');
  const workflow=await createWorkflow('real-delivery',[
    script('fixture',fixture),
    agentNode('plan',plan,{artifactOutputs:[artifact('plan','plan.md')]}),
    agentNode('implement',implement,{artifactOutputs:[artifact('implementation','implementation.md'),artifact('code','mathutil.py','text/x-python')]}),
    agentNode('review',review,{artifactOutputs:[artifact('review','review.md')]}),
    script('verify','set -eu\npython3 -m unittest test_mathutil -v > verification.txt 2>&1\ngrep -q APPROVED review.md\nsha256sum mathutil.py > code.sha256',{artifactOutputs:[artifact('verification','verification.txt','text/plain')]}),
    approval('accept','implement'),
    agentNode('delivery',deliver,{artifactOutputs:[artifact('delivery','delivery.md')]}),
    script('confirm','set -eu\npython3 -m unittest test_mathutil\nsha256sum -c code.sha256'),
  ]);
  const {id}=await startRun(workflow,'修复 clamp 的上下界截断，完成独立评审、测试和人工验收后交付。');
  const waiting=await waitRun(id,r=>r.status==='waiting',{label:'real delivery awaiting human acceptance',timeoutMs:720_000});
  for(const node of ['plan','implement','review','verify'])assert.equal(nodeOf(waiting,node).status,'succeeded',node);
  assert.equal(nodeOf(waiting,'accept').status,'waiting_approval');
  const code=waiting.artifacts.find(a=>a.name==='code');assert.ok(code?.sha256);
  const reviewArtifact=waiting.artifacts.find(a=>a.name==='review');assert.ok(reviewArtifact);
  const reviewText=await api('GET',`/api/runs/${id}/artifacts/${encodeURIComponent(reviewArtifact.id)}`,undefined,{raw:true});
  assert.match(reviewText.text,/APPROVED/);
  await post(`/api/runs/${id}/approve`,{nodeId:'accept',context:'Verified independent review, three real Python tests, and the code artifact.'});
  const done=await waitRun(id,r=>r.status==='succeeded',{label:'real delivery completed',timeoutMs:150_000});
  assert.equal(nodeOf(done,'delivery').status,'succeeded');
  assert.equal(nodeOf(done,'confirm').status,'succeeded');
  assert.ok(done.artifacts.find(a=>a.name==='delivery'));
  assert.equal(done.artifacts.find(a=>a.name==='code').sha256,code.sha256);
  for(const node of ['plan','implement','review','delivery'])assert.ok(nodeOf(done,node).usage?.totalTokens>0,`${node}: no real model usage`);
  console.log('# Real delivery: four model roles, three passing Python tests, approved human gate, unchanged code hash after delivery.');
});
