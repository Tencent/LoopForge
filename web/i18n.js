// Runtime UI translation. The app renders Chinese copy directly; in English or
// Korean mode this layer rewrites text nodes and a few attributes as they appear,
// using the zh→en / zh→ko dictionary. Chinese mode is a no-op.
(() => {
  const root = document.documentElement;
  const rawLang = root.dataset.lang;
  const lang = (rawLang === 'en' || rawLang === 'ko') ? rawLang : 'zh';
  window.lfLang = () => lang;
  window.lfSetLang = next => {
    localStorage.setItem('loopforge.lang', (next === 'en' || next === 'ko') ? next : 'zh');
    location.reload();
  };
  window.lfT = (zh, en, ko) => (lang === 'ko' ? (ko || (dict && dict[zh]) || (typeof translate === 'function' ? translate(zh) : zh) || zh) : (lang === 'en' ? (en || zh) : zh));
  if (lang !== 'en' && lang !== 'ko') return;

  const dict = (lang === 'ko' ? window.LF_I18N_KO : window.LF_I18N_EN) || {};
  const HAN = /[\u3400-\u9fff\u300c\u300d\uff0c\u3002\uff1a\uff08\uff09\u3001\uff1b\uff01\uff1f]/;
  const SKIP = new Set(['SCRIPT', 'STYLE', 'TEXTAREA', 'CODE', 'PRE', 'NOSCRIPT']);
  const SKIP_SEL = '[data-i18n-skip],.monaco-editor,.xterm,.log-line,.run-log,.markdown-body,[contenteditable="true"]';
  // User-authored text only gets whole-string dictionary hits, never fragment
  // rewrites, so a sentence typed by a person is not turned into mixed prose.
  const DATA_SEL = '[data-i18n-data],.ai-user-bubble,.ai-think-body,.collab-process-body,.collab-list-item,.issue-title-cell b,#issueDetailTitle,.detail-desc,.comment-row p';
  const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];
  const escape = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const fragments = Object.keys(dict).filter(k => k.length > 1).sort((a, b) => b.length - a.length);
  const fragmentRE = fragments.length ? new RegExp('(?<![\\u3400-\\u9fff])(?:' + fragments.map(escape).join('|') + ')(?![\\u3400-\\u9fff])', 'g') : null;
  const sentences = fragments.filter(k => k.length >= 6 && HAN.test(k));
  const sentenceRE = sentences.length ? new RegExp(sentences.map(escape).join('|'), 'g') : null;
  const punct = [[/，/g, ', '], [/。/g, '. '], [/：/g, ': '], [/；/g, '; '], [/、/g, ', '], [/！/g, '! '], [/？/g, '? '], [/（/g, ' ('], [/）/g, ') '], [/「|」/g, '"'], [/『|』/g, '"']];
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const UNITS_EN = {次: ['time', 'times'], 步: ['step', 'steps'], 个: ['', ''], 台: ['', ''], 条: ['', '']};
  const UNITS_KO = {次: '회', 步: '단계', 个: '개', 台: '대', 条: '개'};
  
  const patterns = lang === 'ko' ? [
    [/整体\s*(\d+)\s*次执行\s*·\s*(\d+(?:\.\d+)?[kKmMbB]?\s*tokens?)\s*·\s*周期\s*([^<·\n]+)/g, '총 $1회 실행 · $2 · 소요 시간 $3'],
    [/整体\s*(\d+)\s*次执行/g, '총 $1회 실행'],
    [/单次执行平均\s*([^<·\n]+)/g, '1회 실행 평균 $1'],
    [/单次执行/g, '1회 실행'],
    [/首次执行/g, '첫 실행'],
    [/每次执行/g, '매 실행'],
    [/最后一次执行/g, '마지막 실행'],
    [/本次执行/g, '이번 실행'],
    [/这次执行/g, '이번 실행'],
    [/全部执行/g, '전체 실행'],
    [/(\d+)\s*次执行/g, '$1회 실행'],
    [/次执行/g, '회 실행'],
    [/周期\s*([—\-])/g, '소요 시간 $1'],
    [/周期\s*(\d+(?:\.\d+)?[smhd분초시간일]?)/g, '소요 시간 $1'],
    [/(\d+)\s*次\s*·\s*(\d+(?:\.\d+)?[kKmMbB]?\s*tokens?)/g, '$1회 · $2'],
    [/第\s*(\d+)\s*(?:阶段|Stage)/g, '$1단계'],
    [/阶段\s*(\d+)/g, '$1단계'],
    [/待执行\s*·\s*attempt\s*(\d+)/g, '실행 대기 · attempt $1'],
    [/待执行/g, '실행 대기'],
    [/第\s*(\d+)\s*(?:轮|Round)/g, '$1회차'],
    [/ · (高|中|低) · /g, (_, p) => ` · ${{高: '높음', 中: '중간', 低: '낮음'}[p]} · `],
    [/入\s*(\d+)\s*\/\s*出\s*(\d+)/g, '입력 $1 / 출력 $2'],
    [/(\d{4})年(\d{1,2})月/g, '$1년 $2월'],
    [/(^|[^\d])(\d{1,2})月(?![\u3400-\u9fff])/g, '$1$2월'],
    [/共\s*(\d+)\s*(?=[A-Za-z가-힣0-9])/g, '총 $1 '],
    [/(\d{4}-\d{2}-\d{2})\s*至\s*(\d{4}-\d{2}-\d{2})/g, '$1 ~ $2'],
    [/(\d+(?:\.\d+)?)\s*(次|步|个|台|条)(?![\u3400-\u9fff])/g, (_, n, u) => `${n}${UNITS_KO[u] || u}`],
    [/执行机器上的\s*(.+?)\s*本机登录态不可用[，,]\s*请检查\s*Runner\s*账户的\s*CLI\s*登录/g, '실행 머신의 $1 로컬 로그인 세션을 사용할 수 없습니다. Runner 계정의 CLI 로그인을 확인해 주세요'],
    [/执行机器上的\s*(.+?)\s*本机登录态无效或已过期[，,]\s*请检查\s*Runner\s*账户的\s*CLI\s*登录/g, '실행 머신의 $1 로컬 로그인 세션이 유효하지 않거나 만료되었습니다. Runner 계정의 CLI 로그인을 확인해 주세요'],
    [/执行机器上的\s*(.+?)\s*登录态不可用[，,]\s*请检查\s*CLI\s*版本和账号登录/g, '실행 머신의 $1 로그인 세션을 사용할 수 없습니다. CLI 버전 및 계정 로그인을 확인해 주세요'],
    [/执行机器上的\s*CLI\s*本机登录态不可用[，,]\s*请检查执行机器上的\s*CLI\s*登录/g, '실행 머신의 CLI 로컬 로그인 세션을 사용할 수 없습니다. 실행 머신의 CLI 로그인을 확인해 주세요'],
    [/请在运行\s*Runner\s*的系统账户下检查\s*CLI\s*登录[，,]\s*或切换到个人凭证[。.]?/g, 'Runner를 실행 중인 시스템 계정에서 CLI 로그인을 확인하거나 개인 자격 증명으로 전환하세요.'],
    [/执行机器未安装\s*CLI\s*(.+?)(?:[：:]\s*(.+))?$/g, '실행 머신에 CLI $1이(가) 설치되어 있지 않습니다'],
    [/获取模型列表失败[：:]\s*(.+)/g, '모델 목록 가져오기 실패: $1'],
    [/无法连接\s*(.+?)\s*查询模型/g, '$1에 연결하여 모델을 조회할 수 없습니다'],
    [/无法读取\s*(.+?)\s*的模型列表/g, '$1의 모델 목록을 읽을 수 없습니다'],
    [/无法启动\s*(.+?)\s*查询模型/g, '모델 조회를 위해 $1을(를) 시작할 수 없습니다'],
    [/查询\s*(.+?)\s*模型超时/g, '$1 모델 조회 시간 초과'],
    [/(.+?)\s*无法返回账号模型[，,]\s*请检查执行机器上的\s*CLI\s*版本和登录状态/g, '$1에서 계정 모델을 반환할 수 없습니다. 실행 머신의 CLI 버전 및 로그인 상태를 확인해 주세요'],
    [/无法准备\s*(.+?)\s*的本机登录态/g, '$1의 로컬 로그인 세션을 준비할 수 없습니다'],
    [/请先填写\s*(.+?)\s*API Key/g, '먼저 $1 API Key를 입력해 주세요'],
    [/获取账号模型需要\s*Runner\s*(.+?)\s*或以上版本/g, '계정 모델을 가져오려면 Runner $1 이상의 버전이 필요합니다'],
    [/(\d+)\s*成功\s*\/\s*(\d+)\s*失败\s*·\s*仅计算已完成事件/g, '$1 성공 / $2 실패 · 완료된 이벤트만 계산'],
    [/(\d+)\s*人工决策\s*[¶÷]\s*(\d+)\s*平台内执行\s*·\s*暂停\/恢复除外/g, '$1 인간 의사결정 ¶ $2 플랫폼 내 실행 · 일시중지/재개 제외'],
    [/(\d+)\s*完全自动成功\s*\/\s*(\d+)\s*平台内成功/g, '$1 완전 자동 성공 / $2 플랫폼 내 성공'],
    [/无失败[，,]\s*尽情奔跑/g, '실패 없음, 원활하게 진행 중'],
    [/TAPD\s*\/\s*需求\s*→\s*Workflow_task_id\s*→\s*run_id\/trace_id[；;]\s*点击行查看事件详情[。.]?/g, 'TAPD / 요구사항 → Workflow_task_id → run_id/trace_id; 행을 클릭하면 이벤트 세부정보를 볼 수 있습니다.'],
    [/按月对比[；:;\uff1a\uff1b]\s*点击某月(?:份)?查看(?:该时段指标|对应明细)/g, '월별 비교: 특정 월을 클릭하여 해당 기간 지표/세부 정보 보기'],
    [/样本不足[，,]\s*暂无对比/g, '샘플이 부족하여 아직 비교 불가'],
    [/给\s*(.+?)\s*起个名字/g, (_, k) => (dict[k] || k) + ' 이름 입력'],
    [/创建\s*(沙箱|Dockerfile|规则|技能|环境安装脚本|安装脚本)/g, (_, k) => (dict[k] || k) + ' 생성'],
    [/新建\s*(沙箱|Dockerfile|规则|技能|环境安装脚本|安装脚本)/g, (_, k) => '새 ' + (dict[k] || k) + ' 만들기'],
    [/删除\s*(沙箱|Dockerfile|规则|技能|环境安装脚本|安装脚本|角色)/g, (_, k) => (dict[k] || k) + ' 삭제'],
    [/(共享|本项目)\s*(规则|技能|沙箱|Dockerfile|环境安装脚本)\s*(\d+)/g, (_, scope, kind, n) => (scope === '共享' ? '공유 ' : '프로젝트 ') + (dict[kind] || kind) + ' ' + n + '개'],
    [/(共享|本项目)\s*(规则|技能|沙箱|Dockerfile|环境安装脚本)/g, (_, scope, kind) => (scope === '共享' ? '공유 ' : '프로젝트 ') + (dict[kind] || kind)],
    [/第\s*(\d+)\s*步[，,]\s*共\s*(\d+)\s*步/g, '$1단계 / 총 $2단계'],
    [/第\s*(\d+)\s*단계[，,]\s*총\s*(\d+)\s*단계/g, '$1단계 / 총 $2단계'],
    [/第\s*(\d+)\s*步/g, '$1단계'],
    [/共\s*(\d+)\s*步/g, '총 $1단계'],
    [/(\d+)\s*个角色引用/g, '역할 참조 $1개'],
    [/(\d+)\s*个沙箱引用/g, '샌드박스 참조 $1개'],
    [/(\d+)\s*个流程引用/g, 'SOP 참조 $1개'],
    [/(\d+)\s*条规则(?![\u3400-\u9fff])/g, '규칙 $1개'],
    [/(\d+)\s*规则(?![\u3400-\u9fff])/g, '규칙 $1개'],
    [/来自\s*平台/g, '플랫폼 제공'],
    [/来自\s*(.+)/g, '$1 제공'],
    [/未收到\s*submit_gate_result[，,]\s*恢复\s*CLI\s*session\s*后仍未提交/g, 'submit_gate_result를 수신하지 못했습니다. CLI 세션을 복원한 후에도 제출되지 않았습니다.'],
    [/未调用\s*submit_gate_result[，,]\s*且执行器没有返回可恢复的\s*CLI\s*session\s*ID/g, 'submit_gate_result가 호출되지 않았으며, 실행기가 복원 가능한 CLI 세션 ID를 반환하지 않았습니다.'],
    [/未调用\s*submit_gate_result[，,]\s*恢复同一\s*CLI\s*session\s*要求重新提交/g, 'submit_gate_result가 호출되지 않아 동일한 CLI 세션을 복원하여 재제출을 요청합니다.'],
    [/未调用\s*submit_gate_result/g, 'submit_gate_result가 호출되지 않았습니다.'],
    [/退回上游\s*[「"“](.+?)[」"”]\s*并重跑后续/g, '“$1” 단계로 반려 후 후속 작업 재수행'],
    [/卡在\s*[「"“](.+?)[」"”]/g, '“$1” 단계에서 대기'],
    [/暂停于\s*[「"“](.+?)[」"”]/g, '“$1” 단계에서 일시 중지'],
    [/^\s*条待确认/g, '건 확인 대기'],
    [/(\d+)\s*条待确认/g, '$1건 확인 대기'],
    [/全选可审批的\s*(\d+)\s*条/g, '승인 가능한 $1건 전체 선택'],
    [/已选\s*(\d+)\s*条/g, '$1건 선택됨'],
    [/批量通过\s*(\d+)\s*条/g, '$1건 일괄 승인'],
    [/确认通过\s*(\d+)\s*条/g, '$1건 승인 확인'],
    [/已批量通过\s*(\d+)\s*条/g, '$1건 일괄 승인 완료'],
    [/已通过\s*(\d+)\s*条/g, '$1건 승인 완료'],
    [/批量退回\s*(\d+)\s*条/g, '$1건 일괄 반려'],
    [/已退回\s*(\d+)\s*条/g, '$1건 반려 완료'],
    [/跳过\s*(\d+)\s*条/g, '$1건 건너뜀'],
    [/(\d+)\s*条失败/g, '$1건 실패'],
    [/已等待\s*(\d+)\s*(?:分钟|분)前/g, '대기 시간: $1분'],
    [/已等待\s*(\d+)\s*(?:小时|시간)前/g, '대기 시간: $1시간'],
    [/已等待\s*(\d+)\s*(?:天|일)前/g, '대기 시간: $1일'],
    [/已等待\s*(\d+)\s*(?:秒|초)前/g, '대기 시간: $1초'],
    [/已等待\s*刚刚/g, '방금 전부터 대기'],
    [/已等待\s*(.+)/g, '대기 시간: $1'],
    [/(\d+)\s*分钟前/g, '$1분 전'],
    [/(\d+)\s*小时前/g, '$1시간 전'],
    [/(\d+)\s*天前/g, '$1일 전'],
    [/(\d+)\s*秒前/g, '$1초 전'],
  ] : [
    [/整体\s*(\d+)\s*次执行\s*·\s*(\d+(?:\.\d+)?[kKmMbB]?\s*tokens?)\s*·\s*周期\s*([^<·\n]+)/g, 'Overall $1 runs · $2 · Cycle $3'],
    [/整体\s*(\d+)\s*次执行/g, 'Overall $1 runs'],
    [/单次执行平均\s*([^<·\n]+)/g, 'Average per run $1'],
    [/单次执行/g, 'Single run'],
    [/首次执行/g, 'First run'],
    [/每次执行/g, 'Each run'],
    [/最后一次执行/g, 'Last run'],
    [/本次执行/g, 'Current run'],
    [/这次执行/g, 'This run'],
    [/全部执行/g, 'All runs'],
    [/(\d+)\s*次执行/g, '$1 runs'],
    [/次执行/g, ' runs'],
    [/周期\s*([—\-])/g, 'Cycle $1'],
    [/周期\s*(\d+(?:\.\d+)?[smhd]?)/g, 'Cycle $1'],
    [/(\d+)\s*次\s*·\s*(\d+(?:\.\d+)?[kKmMbB]?\s*tokens?)/g, '$1 runs · $2'],
    [/第\s*(\d+)\s*(?:阶段|Stage)/g, 'Stage $1'],
    [/阶段\s*(\d+)/g, 'Stage $1'],
    [/待执行\s*·\s*attempt\s*(\d+)/g, 'Pending · attempt $1'],
    [/待执行/g, 'Pending'],
    [/第\s*(\d+)\s*(?:轮|Round)/g, 'Round $1'],
    [/ · (高|中|低) · /g, (_, p) => ` · ${{高: 'High', 中: 'Medium', 低: 'Low'}[p]} · `],
    [/入\s*(\d+)\s*\/\s*出\s*(\d+)/g, 'In $1 / Out $2'],
    [/(\d{4})年(\d{1,2})月/g, (_, y, m) => `${MONTHS[m - 1] || m} ${y}`],
    [/(^|[^\d])(\d{1,2})月(?![\u3400-\u9fff])/g, (_, pre, m) => pre + (MONTHS[m - 1] || m)],
    [/共\s*(\d+)\s*(?=[A-Za-z])/g, '$1 '],
    [/(\d{4}-\d{2}-\d{2})\s*至\s*(\d{4}-\d{2}-\d{2})/g, '$1 – $2'],
    [/(\d+(?:\.\d+)?)\s*(次|步|个|台|条)(?![\u3400-\u9fff])/g, (_, n, u) => {
      const word = UNITS_EN[u][Number(n) === 1 ? 0 : 1];
      return word ? `${n} ${word}` : n;
    }],
    [/执行机器上的\s*(.+?)\s*本机登录态不可用[，,]\s*请检查\s*Runner\s*账户的\s*CLI\s*登录/g, 'Local login session for $1 on runner machine is unavailable. Please check the runner account\'s CLI login'],
    [/执行机器上的\s*(.+?)\s*本机登录态无效或已过期[，,]\s*请检查\s*Runner\s*账户的\s*CLI\s*登录/g, 'Local login session for $1 on runner machine is invalid or expired. Please check the runner account\'s CLI login'],
    [/执行机器上的\s*(.+?)\s*登录态不可用[，,]\s*请检查\s*CLI\s*版本和账号登录/g, 'Login session for $1 on runner machine is unavailable. Please check CLI version and account login'],
    [/执行机器上的\s*CLI\s*本机登录态不可用[，,]\s*请检查执行机器上的\s*CLI\s*登录/g, 'CLI local login session on runner machine is unavailable. Please check CLI login on runner machine'],
    [/请在运行\s*Runner\s*的系统账户下检查\s*CLI\s*登录[，,]\s*或切换到个人凭证[。.]?/g, 'Please check CLI login under the runner system account, or switch to personal credentials.'],
    [/执行机器未安装\s*CLI\s*(.+?)(?:[：:]\s*(.+))?$/g, 'Runner machine does not have CLI $1 installed'],
    [/获取模型列表失败[：:]\s*(.+)/g, 'Failed to fetch model list: $1'],
    [/给\s*(.+?)\s*起个名字/g, (_, k) => 'Name this ' + (dict[k] || k)],
    [/创建\s*(沙箱|Dockerfile|规则|技能|环境安装脚本|安装脚本)/g, (_, k) => 'Create ' + (dict[k] || k)],
    [/新建\s*(沙箱|Dockerfile|规则|技能|环境安装脚本|安装脚本)/g, (_, k) => 'New ' + (dict[k] || k)],
    [/第\s*(\d+)\s*步[，,]\s*共\s*(\d+)\s*步/g, 'Step $1 of $2'],
    [/(\d+)\s*个角色引用/g, '$1 role reference(s)'],
    [/来自\s*平台/g, 'From Platform'],
    [/未收到\s*submit_gate_result[，,]\s*恢复\s*CLI\s*session\s*后仍未提交/g, 'Did not receive submit_gate_result; still not submitted after resuming CLI session'],
    [/未调用\s*submit_gate_result[，,]\s*且执行器没有返回可恢复的\s*CLI\s*session\s*ID/g, 'submit_gate_result was not called and executor did not return a resumable CLI session ID'],
    [/未调用\s*submit_gate_result/g, 'submit_gate_result was not called'],
    [/退回上游\s*[「"“](.+?)[」"”]\s*并重跑后续/g, 'Send back to “$1” and rerun downstream'],
    [/卡在\s*[「"“](.+?)[」"”]/g, 'Waiting at “$1”'],
    [/暂停于\s*[「"“](.+?)[」"”]/g, 'Paused at “$1”'],
    [/^\s*条待确认/g, ' items pending approval'],
    [/(\d+)\s*条待确认/g, '$1 pending approval'],
    [/全选可审批的\s*(\d+)\s*条/g, 'Select all $1 approvable'],
    [/已选\s*(\d+)\s*条/g, '$1 selected'],
    [/批量通过\s*(\d+)\s*条/g, 'Batch approve $1'],
    [/确认通过\s*(\d+)\s*条/g, 'Confirm approve $1'],
    [/已批量通过\s*(\d+)\s*条/g, 'Batch approved $1'],
    [/已通过\s*(\d+)\s*条/g, '$1 approved'],
    [/批量退回\s*(\d+)\s*条/g, 'Batch reject $1'],
    [/已退回\s*(\d+)\s*条/g, '$1 rejected'],
    [/跳过\s*(\d+)\s*条/g, 'Skipped $1'],
    [/(\d+)\s*条失败/g, '$1 failed'],
    [/已等待\s*(\d+)\s*分钟前/g, 'Waiting: $1m'],
    [/已等待\s*(\d+)\s*小时前/g, 'Waiting: $1h'],
    [/已等待\s*(\d+)\s*天前/g, 'Waiting: $1d'],
    [/已等待\s*(.+)/g, 'Waiting: $1'],
    [/(\d+)\s*分钟前/g, '$1m ago'],
    [/(\d+)\s*小时前/g, '$1h ago'],
    [/(\d+)\s*天前/g, '$1d ago'],
  ];
  const cache = new Map();

  function translateExact(text) {
    const trimmed = text && text.trim();
    return trimmed && Object.prototype.hasOwnProperty.call(dict, trimmed) ? text.replace(trimmed, dict[trimmed]) : text;
  }

  function translate(text) {
    if (!text || !HAN.test(text)) return text;
    const hit = cache.get(text);
    if (hit !== undefined) return hit;
    const trimmed = text.trim();
    let out;
    if (Object.prototype.hasOwnProperty.call(dict, trimmed)) {
      out = text.replace(trimmed, dict[trimmed]);
    } else {
      out = text;
      for (const [re, rep] of patterns) out = out.replace(re, rep);
      if (HAN.test(out)) {
        if (fragmentRE) out = out.replace(fragmentRE, m => dict[m]);
        if (sentenceRE && HAN.test(out)) out = out.replace(sentenceRE, m => dict[m]);
      }
    }
    if (out !== text) {
      for (const [re, rep] of punct) out = out.replace(re, rep);
      out = out.replace(/:\s*:/g, ':').replace(/ {2,}/g, ' ').replace(/ ([,.:;!?)])/g, '$1').replace(/\( /g, '(');
      if (/^\s/.test(text) === false) out = out.replace(/^\s+/, '');
      if (/\s$/.test(text) === false) out = out.replace(/\s+$/, '');
    }
    if (cache.size > 20000) cache.clear();
    cache.set(text, out);
    return out;
  }
  window.lfTranslate = translate;

  function skipped(el) {
    return !el || SKIP.has(el.tagName) || (el.closest && el.closest(SKIP_SEL));
  }
  function translateText(node) {
    const parent = node.parentElement;
    if (skipped(parent)) return;
    const next = parent.closest(DATA_SEL) ? translateExact(node.nodeValue) : translate(node.nodeValue);
    if (next !== node.nodeValue) node.nodeValue = next;
  }
  function translateAttrs(el) {
    if (el.closest && el.closest(SKIP_SEL)) return;
    for (const name of ATTRS) {
      const value = el.getAttribute && el.getAttribute(name);
      if (value && HAN.test(value)) {
        const next = el.closest(DATA_SEL) ? translateExact(value) : translate(value);
        if (next !== value) el.setAttribute(name, next);
      }
    }
    if (el.tagName === 'INPUT' && /^(button|submit|reset)$/i.test(el.type) && HAN.test(el.value)) el.value = translate(el.value);
  }
  function walk(node) {
    if (node.nodeType === 3) return translateText(node);
    if (node.nodeType !== 1) return;
    if (skipped(node)) { if (node.tagName === 'TEXTAREA') translateAttrs(node); return; }
    translateAttrs(node);
    const tw = document.createTreeWalker(node, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
      acceptNode: n => {
        if (n.nodeType !== 1 || !skipped(n)) return NodeFilter.FILTER_ACCEPT;
        if (n.tagName === 'TEXTAREA') translateAttrs(n);
        return NodeFilter.FILTER_REJECT;
      },
    });
    let n;
    while ((n = tw.nextNode())) {
      if (n.nodeType === 3) translateText(n);
      else translateAttrs(n);
    }
  }
  function translateTitle() {
    const next = translate(document.title);
    if (next !== document.title) document.title = next;
  }

  const observer = new MutationObserver(records => {
    for (const r of records) {
      if (r.type === 'childList') r.addedNodes.forEach(walk);
      else if (r.type === 'characterData') translateText(r.target);
      else if (r.type === 'attributes') translateAttrs(r.target);
    }
  });
  function start() {
    walk(document.body);
    translateTitle();
    observer.observe(document.body, {subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS});
    const title = document.querySelector('title');
    if (title) new MutationObserver(translateTitle).observe(title, {childList: true, characterData: true, subtree: true});
  }
  if (document.body) start();
  else document.addEventListener('DOMContentLoaded', start, {once: true});

  const nativeConfirm = window.confirm.bind(window);
  const nativeAlert = window.alert.bind(window);
  const nativePrompt = window.prompt.bind(window);
  window.confirm = message => nativeConfirm(translate(String(message ?? '')));
  window.alert = message => nativeAlert(translate(String(message ?? '')));
  window.prompt = (message, value) => nativePrompt(translate(String(message ?? '')), value);
})();
