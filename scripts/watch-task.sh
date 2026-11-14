#!/usr/bin/env bash
# 跟踪一个任务的进度/ETA，直到终态。用法：watch-task.sh <taskID> [间隔秒]
set -u
API=${API:-http://localhost:18888/api}
TID=${1:?need task id}
IV=${2:-8}
TOKEN=$(cat "$(dirname "$0")/../.tmp_tok") || { cat "$(dirname "$0")/../.tmp_tok" 2>/dev/null; }
[ -z "$TOKEN" ] && TOKEN=$(cat /d/workbuddy/2026-10-16-13-27-12/asset-platform/.tmp_tok)

while :; do
  curl -s "$API/tasks?page=1&pageSize=5" -H "Authorization: Bearer $TOKEN" | python -c "
import sys,json,time
tid=$TID
d=json.load(sys.stdin)
for r in d.get('items') or []:
    if int(r['id'])!=tid: continue
    q=r.get('queue') or {}
    s=r.get('etaSeconds')
    def f(s):
        if s is None or s<0: return '估算中…'
        if s>=60: return f'{s//60}分{s%60}秒'
        return f'{s}秒'
    print(f\"{time.strftime('%H:%M:%S')} {r['status']:<7} {str(q.get('phase')):<8} pct={r.get('percent'):>4} conf={str(r.get('etaConfidence')):<4} eta={f(s):<10} pps={round(r.get('pps') or 0):>5} units={q.get('done',0)}/{q.get('total',0):<4} alive={r.get('aliveCount')} probe={r.get('probeOffset')}/{r.get('totalIps')}\", flush=True)
    if r['status'] in ('done','failed','interrupted'):
        sys.exit(9)
" || exit 0
  [ $? -eq 9 ] && break
  sleep "$IV"
done
echo "--- 任务日志 ---"
curl -s "$API/tasks/$TID/log" -H "Authorization: Bearer $TOKEN" | python -c "
import sys,json
for l in json.load(sys.stdin).get('lines',[]): print(l)"
