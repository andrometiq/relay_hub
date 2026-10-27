"""Bounded loopback-only API benchmark. No provider calls or database writes."""
import concurrent.futures
import http.client
import json
import math
from pathlib import Path
import statistics
import time

ROOT=Path(__file__).resolve().parents[1]
config={}
for line in (ROOT/'.env').read_text().splitlines():
    if '=' in line and not line.lstrip().startswith('#'):
        k,v=line.split('=',1);config[k.strip()]=v.strip().strip('\"\'')
token=config['RELAY_MCP_TOKEN']
pid=int((ROOT/'.runtime/hub.pid').read_text())

def rss():
    for line in Path(f'/proc/{pid}/status').read_text().splitlines():
        if line.startswith('VmRSS:'):return int(line.split()[1])

operations={
 'health':('GET','/healthz',None,False,200),
 'readiness':('GET','/readyz',None,False,200),
 'mcp_initialize':('POST','/mcp',{'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2025-06-18','capabilities':{},'clientInfo':{'name':'relay-benchmark','version':'1'}}},True,200),
 'mcp_tools_list':('POST','/mcp',{'jsonrpc':'2.0','id':2,'method':'tools/list','params':{}},True,200),
 'mcp_health':('POST','/mcp',{'jsonrpc':'2.0','id':3,'method':'tools/call','params':{'name':'health.get','arguments':{}}},True,200),
 'mcp_unauthorized':('POST','/mcp',{},False,401),
}
results=[]
for name,(method,path,body,auth,expected) in operations.items():
 for concurrency in (1,5,15):
    durations=[];errors=[]
    def worker(_):
        conn=http.client.HTTPConnection('127.0.0.1',18081,timeout=5)
        measured=[];failed=[]
        headers={'Content-Type':'application/json','Accept':'application/json, text/event-stream','MCP-Protocol-Version':'2025-06-18'}
        if auth:headers['Authorization']='Bearer '+token
        for _ in range(30):
            start=time.perf_counter()
            try:
                conn.request(method,path,json.dumps(body) if body is not None else None,headers)
                response=conn.getresponse();raw=response.read()
                ok=response.status==expected
                if ok and expected==200:
                    payload=json.loads(raw)
                    ok=not payload.get('error') and not payload.get('result',{}).get('isError')
                failed.append(not ok)
            except Exception:
                failed.append(True);conn.close()
            measured.append((time.perf_counter()-start)*1000)
        conn.close();return measured,failed
    before=rss();start=time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        for measured,failed in pool.map(worker,range(concurrency)):
            durations+=measured;errors+=failed
    elapsed=time.perf_counter()-start;durations.sort()
    item=dict(operation=name,concurrency=concurrency,requests=len(durations),errors=sum(errors),seconds=round(elapsed,3),rps=round(len(durations)/elapsed,1),p50_ms=round(statistics.median(durations),3),p95_ms=round(durations[math.ceil(.95*len(durations))-1],3),p99_ms=round(durations[math.ceil(.99*len(durations))-1],3),rss_before_kib=before,rss_after_kib=rss())
    results.append(item);print(json.dumps(item),flush=True)
    time.sleep(.15)
(ROOT/'docs/performance/local-api-results.json').write_text(json.dumps({'scope':'loopback smoke benchmark; not production capacity or provider throughput','results':results},indent=2)+'\n')
