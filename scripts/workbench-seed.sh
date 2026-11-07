#!/usr/bin/env bash
# Seeds the local requirement-space preview started by workbench-dev.sh.
# Run by hand only. Preview-only rows (people, the deterministic mock CLI and
# the roles that use it) go straight into the preview MySQL, because the
# public API only offers real CLIs; everything else goes through the API.
set -euo pipefail

API="${LOOPFORGE_API:-http://127.0.0.1:18765}"
MYSQL_CONTAINER="${LF_MYSQL_CONTAINER:-lf-workbench-mysql}"
PROJECT="${LF_PROJECT:-proj-mall}"
export LF_SESSION_SECRET="${LF_SESSION_SECRET:-lf-workbench-preview}"

sql() { docker exec -i "$MYSQL_CONTAINER" mysql --default-character-set=utf8mb4 -uroot -proot loopforge 2>/dev/null; }

curl -fsS "$API/api/auth/current" >/dev/null || { echo "API $API 不可用，先运行 scripts/workbench-dev.sh" >&2; exit 1; }

python3 - "$API" "$PROJECT" <<'PY' | sql
import json, sys
project = sys.argv[2]
now = "NOW(3)"
people = [(9001, "linxia", "林夏", "产品"), (9002, "zhouyu", "周宇", "设计"), (9003, "chenmo", "陈默", "前端"), (9004, "xunuo", "许诺", "后端"), (9005, "gaoyang", "高阳", "测试"),
          (9100, "tester", "自测账号", "自测")]
print("SET NAMES utf8mb4;")
for uid, login, cn, dept in people:
    print(f"INSERT INTO users (id, staff_id, login_name, chinese_name, dept_id, dept_name, platform_role, created_at, updated_at) VALUES ({uid}, {uid}, '{login}', '{cn}', 0, '{dept}', 'user', {now}, {now}) ON DUPLICATE KEY UPDATE chinese_name=VALUES(chinese_name), dept_name=VALUES(dept_name);")
ex = {"id": "mock", "name": "演示执行器", "kind": "mock", "binary": "", "args": [], "protocol": "events-v1"}
print("INSERT INTO executors (id, name, kind, payload) VALUES ('mock', '演示执行器', 'mock', %s) ON DUPLICATE KEY UPDATE payload=VALUES(payload);" % json.dumps(json.dumps(ex, ensure_ascii=False), ensure_ascii=False))
agents = [
    ("wb-fe-architect", "前端架构师", "拆分页面与组件，给出前端方案"),
    ("wb-fe-dev", "前端开发", "按方案实现页面与交互"),
    ("wb-be-architect", "后端架构师", "设计接口与数据模型"),
    ("wb-be-dev", "后端开发", "实现接口、迁移与单测"),
    ("wb-qa", "测试工程师", "编写并执行验收用例"),
]
analyst = ("wb-analyst", "需求分析师", "读需求，产出一份结构化的需求分析：目标、范围、验收标准、风险和待确认问题",
    "你是需求分析师。只根据任务输入里的需求内容工作，不需要读写代码仓库。\n"
    "输出一份 Markdown 需求分析，包含：1. 目标（一句话）2. 范围（做什么/不做什么）3. 验收标准（可测试的条目）4. 风险 5. 待确认问题。\n"
    "写完后把这份分析作为你的最终回复。")
for aid, name, desc in agents + [analyst[:3]]:
    executor = "codebuddy" if aid == analyst[0] else "mock"
    a = {"id": aid, "name": name, "version": 1, "projectId": project, "description": desc, "instructions": analyst[3] if aid == analyst[0] else desc, "executorId": executor}
    print("INSERT INTO agents (id, name, payload, updated_at) VALUES ('%s', '%s', %s, %s) ON DUPLICATE KEY UPDATE name=VALUES(name), payload=VALUES(payload), updated_at=VALUES(updated_at);" % (aid, name, json.dumps(json.dumps(a, ensure_ascii=False), ensure_ascii=False), now))
PY

python3 - "$API" "$PROJECT" <<'PY'
import base64, datetime, hashlib, hmac, json, os, sys, urllib.request, urllib.error
api, project = sys.argv[1], sys.argv[2]
people = {9001: ("linxia", "林夏"), 9002: ("zhouyu", "周宇"), 9003: ("chenmo", "陈默"), 9004: ("xunuo", "许诺"), 9005: ("gaoyang", "高阳")}

def cookie(uid):
    # Same format as auth.SignUser, signed with the preview's session secret.
    login, cn = people[uid]
    payload = json.dumps({"id": uid, "staffId": uid, "loginName": login, "chineseName": cn, "deptId": 0, "deptName": "", "isGuest": False,
                          "authTime": datetime.datetime.now(datetime.timezone.utc).isoformat()}, ensure_ascii=False, separators=(",", ":")).encode()
    b64 = base64.urlsafe_b64encode(payload).rstrip(b"=").decode()
    sig = hmac.new(os.environ["LF_SESSION_SECRET"].encode(), b64.encode(), hashlib.sha256).hexdigest()
    return f"agentflow_user={b64}.{sig}"

def call(method, path, body=None, ok=(200, 201), who=None):
    data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
    headers = {"Content-Type": "application/json"}
    if who:
        headers["Cookie"] = cookie(who)
    req = urllib.request.Request(api + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        msg = e.read().decode(errors="replace")
        if e.code in ok:
            return json.loads(msg)
        raise SystemExit(f"{method} {path} -> {e.code}: {msg}")

if not any(p.get("id") == project for p in call("GET", "/api/projects") or []):
    call("POST", "/api/projects", {"id": project, "key": "MALL", "name": "积分商城", "description": "积分商城与会员权益"})
proj = call("GET", f"/api/projects/{project}")
proj["ownerId"] = 9001
proj["members"] = [{"userId": uid, "role": "member"} for uid in (9001, 9002, 9003, 9004, 9005, 9100)]
call("PUT", "/api/projects", proj)

call("PUT", "/api/sandboxes", {"id": "wb-local", "name": "本地沙箱", "projectId": project, "driver": "local"})

def sop(wid, name, lane, steps, description=None):
    nodes, edges = [], []
    for i, (nid, label, typ, agent) in enumerate(steps):
        n = {"id": nid, "name": label, "type": typ, "maxAttempts": 1, "maxReworks": 2, "position": {"x": 0, "y": i * 120}}
        if typ == "agent":
            n["agentId"], n["metricStage"] = agent, "development"
        elif typ == "script":
            n["script"], n["scriptLanguage"], n["metricStage"] = "echo '用例 12/12 通过'", "shell", "testing"
        else:
            n["metricStage"] = "none"
        nodes.append(n)
        if i:
            edges.append({"id": f"e{i}", "source": steps[i - 1][0], "target": nid, "when": "success"})
    nodes[1]["rejectTo"] = steps[0][0]
    wf = {"id": wid, "name": name, "version": 1, "status": "published", "projectId": project, "description": description or name + "：方案 → 评审 → 实现 → 验证",
          "sandboxProfileId": "wb-local", "sopLane": lane, "nodes": nodes, "edges": edges}
    old = next((w for w in call("GET", "/api/workflows") if w.get("id") == wid), None)
    if old:
        wf["revision"], wf["version"] = old.get("revision", 0), old.get("version", 1)
    call("PUT", "/api/workflows", wf)

sop("wb-fe-sop", "前端研发 SOP", "frontend", [("fe-plan", "前端方案", "agent", "wb-fe-architect"), ("fe-review", "方案评审", "approval", ""),
    ("fe-dev", "页面实现", "agent", "wb-fe-dev"), ("fe-test", "自动化测试", "script", "")])
sop("wb-be-sop", "后端研发 SOP", "backend", [("be-api", "接口设计", "agent", "wb-be-architect"), ("be-review", "接口评审", "approval", ""),
    ("be-dev", "接口实现", "agent", "wb-be-dev"), ("be-test", "接口测试", "script", "")])
sop("wb-real-analysis", "需求分析 SOP（真实 AI）", "product", [("ra-analyze", "需求分析", "agent", "wb-analyst"), ("ra-review", "确认分析", "approval", "")],
    "用你在设置里配置的 CodeBuddy Key 真实执行：AI 产出需求分析 → 人工确认。不需要代码仓库，适合自测。")

title = "积分商城 · 兑换流程改版"
existing = next((s for s in call("GET", f"/api/spaces?projectId={project}") if s.get("title") == title), None)
if existing:
    print("会话已存在：", existing["id"])
    raise SystemExit(0)

space = call("POST", "/api/spaces", who=9001, body={"projectId": project, "title": title,
    "summary": "把积分兑换从 4 步缩到 2 步：商品页直接选规格并确认，库存不足时给替代推荐。目标兑换转化率 +15%。",
    "creatorTitle": "产品", "members": [{"userId": 9002, "title": "设计"}, {"userId": 9003, "title": "前端"}, {"userId": 9004, "title": "后端"}, {"userId": 9005, "title": "测试"}]})
sid = space["id"]
base = f"/api/spaces/{sid}"
call("POST", base + "/messages", who=9001, body={"body": "这期目标：兑换从 4 步缩到 2 步。验收口径和边界我写在需求说明里了，大家先看一下。"})
doc = call("POST", base + "/artifacts", who=9001, body={"kind": "doc", "title": "需求说明", "format": "markdown", "announce": True,
    "summary": "两步兑换、库存不足替代推荐、积分不足引导",
    "body": "# 积分兑换改版\n\n## 目标\n- 兑换步骤从 4 步缩到 2 步\n- 兑换转化率提升 15%\n\n## 范围\n1. 商品页内选择规格并确认兑换\n2. 库存不足时推荐同价位替代商品\n3. 积分不足时引导去做任务\n\n## 验收\n- [ ] 2 步内完成兑换\n- [ ] 库存不足有替代推荐\n- [ ] 兑换失败可重试且不重复扣分\n"})
thread = call("POST", base + "/threads", who=9003, body={"title": "库存口径"})
call("POST", base + "/messages", who=9003, body={"threadId": thread["id"], "body": "库存按 SKU 计还是按 SPU？替代推荐要用到。"})
call("POST", base + "/messages", who=9001, body={"threadId": thread["id"], "body": "按 SKU，推荐时同 SPU 优先。"})
call("POST", base + "/requirement", who=9001, body={"title": "积分兑换流程改版（4 步 → 2 步）", "priority": "high",
    "description": "把积分兑换从 4 步缩到 2 步：商品页直接选规格并确认，库存不足时给替代推荐。目标兑换转化率 +15%。"})
proto_html = """<!doctype html><html><head><meta charset=utf-8><style>
body{margin:0;font:14px -apple-system,system-ui,sans-serif;background:#f6f6f4;color:#1c1c1a;display:flex;justify-content:center}
.p{width:300px;padding:18px}.card{background:#fff;border-radius:14px;padding:16px;box-shadow:0 1px 2px rgba(0,0,0,.06)}
.img{height:150px;border-radius:10px;background:linear-gradient(135deg,#ffe2c7,#ffc1a6);margin-bottom:12px}
.row{display:flex;gap:8px;margin:12px 0}.sku{flex:1;border:1px solid #e3e3df;border-radius:8px;padding:8px;text-align:center;cursor:pointer}
.sku.on{border-color:#e8590c;color:#e8590c;background:#fff4ec}.btn{width:100%;border:0;border-radius:10px;padding:12px;background:#e8590c;color:#fff;font-weight:600;cursor:pointer}
.ok{text-align:center;padding:30px 0}.muted{color:#888;font-size:12px}</style></head><body><div class=p><div class=card id=app></div></div>
<script>
let sku='标准款',step=1;const app=document.getElementById('app');
function render(){if(step===1){app.innerHTML='<div class=img></div><b>保温杯 · 500ml</b><div class=muted>2,400 积分 · 剩余 32 件</div><div class=row>'+['标准款','礼盒款'].map(s=>'<div class="sku'+(s===sku?' on':'')+'" data-s="'+s+'">'+s+'</div>').join('')+'</div><button class=btn id=go>立即兑换</button>';
app.querySelectorAll('.sku').forEach(e=>e.onclick=()=>{sku=e.dataset.s;render()});document.getElementById('go').onclick=()=>{step=2;render()}}
else{app.innerHTML='<div class=ok><div style="font-size:36px">✓</div><b>兑换成功</b><p class=muted>'+sku+' · 预计 3 天内发货</p><button class=btn id=back>再看看</button></div>';document.getElementById('back').onclick=()=>{step=1;render()}}}
render();
</script></body></html>"""
proto = call("POST", base + "/artifacts", who=9002, body={"kind": "prototype", "title": "兑换流程原型", "format": "html", "announce": True,
    "summary": "商品页内选规格 → 确认兑换，2 步完成", "body": proto_html})
call("POST", base + "/messages", who=9002, body={"body": "原型可以直接在右侧点着试，规格切换和兑换成功页都做了。"})
api_doc = call("POST", base + "/artifacts", who=9004, body={"kind": "api", "title": "兑换接口契约", "format": "markdown", "announce": True,
    "summary": "POST /exchange、GET /exchange/alternatives",
    "body": "## POST /api/points/exchange\n请求：`{ skuId, quantity, idempotencyKey }`\n\n返回：`{ orderId, pointsLeft }`\n\n错误：`STOCK_EMPTY`（附 alternatives）、`POINTS_NOT_ENOUGH`\n\n## GET /api/points/exchange/alternatives?skuId=\n返回同价位、同 SPU 优先的替代商品列表"})
call("POST", base + "/sops", who=9004, body={"workflowIds": ["wb-fe-sop", "wb-be-sop"], "inputs": [doc["id"], proto["id"], api_doc["id"]]})
print("会话已创建：", sid)
PY
echo "预览数据就绪：$API/#/collaboration"
