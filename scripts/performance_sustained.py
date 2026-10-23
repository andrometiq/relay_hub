"""Fixed-rate, bounded local readiness test; no external calls."""
import concurrent.futures, http.client, json, math, threading, time
from pathlib import Path
root=Path(__file__).resolve().parents[1]
results=[]
for rate in (50,100):
    capacity=threading.BoundedSemaphore(15);out=[];dropped=0
    def call(scheduled):
        conn=http.client.HTTPConnection('127.0.0.1',18081,timeout=3)
        try:
            start=time.perf_counter();conn.request('GET','/readyz');r=conn.getresponse();r.read()
            return {'ok':r.status==200,'latency_ms':(time.perf_counter()-start)*1000,'scheduled_latency_ms':(time.perf_counter()-scheduled)*1000}
        except Exception:return {'ok':False,'latency_ms':0,'scheduled_latency_ms':0}
        finally:conn.close();capacity.release()
    start=time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=15) as pool:
        for i in range(rate*15):
            target=start+i/rate;time.sleep(max(0,target-time.perf_counter()))
            if capacity.acquire(blocking=False):out.append(pool.submit(call,target))
            else:dropped+=1
        values=[f.result() for f in out]
    lats=sorted(v['scheduled_latency_ms'] for v in values)
    item={'target_rps':rate,'duration_s':15,'sent':len(values),'admission_drops':dropped,'errors':sum(not v['ok'] for v in values),'scheduled_p95_ms':round(lats[math.ceil(len(lats)*.95)-1],3),'scheduled_p99_ms':round(lats[math.ceil(len(lats)*.99)-1],3)}
    results.append(item);print(json.dumps(item),flush=True)
(root/'docs/performance/fixed-rate-results.json').write_text(json.dumps(results,indent=2)+'\n')
