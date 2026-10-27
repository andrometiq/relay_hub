// Run against built assets. All provider, account and security APIs are local fixtures.
// PLAYWRIGHT_MODULE points to an existing installation; no dependencies are added.
import { createRequire } from 'node:module';
import assert from 'node:assert/strict';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/opt/google/chrome/chrome', headless: true, args: ['--no-sandbox'] });
const page = await browser.newPage({viewport:{width:1440,height:1000}});
const origin = 'http://127.0.0.1:18129';
page.setDefaultTimeout(8000);
const errors = [], writes = [], reads = [];
page.on('pageerror', error => errors.push(error.message));
page.on('console', message => { if(message.type() === 'error' && !message.text().includes('Failed to load resource')) errors.push(message.text()); });
const account = {id:'local-only-review',site:'https://local.invalid',enabled:false,channels:[{channel:'WhatsApp',enabled:true,config:{connection_status:'connected',verified_at:'2026-10-24',groups:true,calls:true,templates:true,sync_metadata:true},configured_secrets:['secret','app_secret','verify_token']},{channel:'Telegram',enabled:true,config:{retained:true}}]};
const number = {key:'local-number',channel:'WhatsApp',container:'local-main',label:'Local number',address:'1001',site:'',enabled:true,config:{phone_id:'1001',waba_id:'2001'},configured_secrets:['secret']};
const container = {id:'local-main',name:'Local main',enabled:true,whatsapp:{config:{app_id:'3001',graph_version:'v23.0'},configured_secrets:['app_secret','verify_token']},channels:[number]};
const site = {id:account.id,origin:'https://local.invalid',enabled:true,configured_secrets:[],channels:[]};
const pickerSite = {id:'review-site',name:'Review site',origin:'https://review.invalid',enabled:true,configured_secrets:[],channels:[{channel:'WhatsApp',key:account.id,label:'Review line',enabled:true,config:{}}]};
let callbackFail = true, callbacks = true, delivery = true;
await page.route('**/auth/tokens**', route => route.fulfill({json:{Tokens:[],More:false}}));
await page.route('**/api/**', async route => {
 const req = route.request(), url = new URL(req.url()), path = url.pathname.split('/api/')[1];
 if(req.method() !== 'GET') {
  const body = req.postDataJSON(); writes.push({method:req.method(),path,body});
  if(path === 'callback-failures/retry') { if(callbackFail) return route.fulfill({status:409,body:'Callback remains quarantined'}); callbacks = false; }
  if(path === 'delivery-queue') delivery = false;
  return route.fulfill({status:204});
 }
 reads.push(path); let data = {};
 if(path === 'accounts') data = {accounts:[account],total:1,csrf:'local-only-csrf'};
 if(path === 'channels') data = {whatsapp:1};
 if(path === 'containers') data = {containers:[container],next:''};
 if(path === 'containers/local-main') data = container;
 if(path === 'sites') data = {sites:[site,pickerSite],next:'',csrf:'local-only-csrf'};
 if(path === `sites/${account.id}`) data = site;
 if(path === 'deliveries') data = {counts:{queued:3,uncertain:2,failed:1}};
 if(path === 'accounts/health') data = {webhook_received_at:'2026-10-24 09:00',core_confirmed_at:'2026-10-24 09:01',callback_pending:3,callback_failed:2,callback_quarantined:1};
 if(path === 'accounts/metadata') data = {status:'ready',last_success:'2026-10-24',profile:{display_phone_number:'+1555010100',verified_name:'Local fixture',quality_rating:'GREEN'},templates:[{}]};
 if(path === 'delivery-queue') data = {deliveries: delivery ? [{id:'41',state:'queued',attempts:2,error:'Local provider failure',provider_error:{code:130429},result:{messages:[{id:'local-provider-id'}]}}] : [],more:false};
 if(path === 'callback-failures') data = {items:callbacks ? [{id:42,lane:'status',reason:'Invalid receipt',failures:3,quarantined:true,created_at:'2026-10-24'}] : [],more:false};
 if(path === 'event-inspector') data = url.searchParams.has('id') ? {raw:'{"local_only":true}',truncated:false} : {items:[{id:43,time:'2026-10-24',kind:'realtime',preview:'Local inbound message',callback:'Forwarded'}],more:false};
 if(path === 'audit') data = {events:[{id:1,action:'account.save',outcome:'completed',request_id:'local-request',occurred_at:'2026-10-24',actor_type:'user'}],more:false};
 await route.fulfill({json:data});
});
async function nav(name) {
 if(await page.getByRole('button',{name:'Toggle navigation'}).isVisible()) await page.getByRole('button',{name:'Toggle navigation'}).click();
 await page.getByRole('button',{name,exact:true}).click();
}
async function noOverflow(label) {
 const overflow = await page.evaluate(() => [...document.querySelectorAll('.hub-shell,.hub-page,.relay-dialog')].filter(el => el.scrollWidth > el.clientWidth + 1).map(el=>({class:el.className,width:el.clientWidth,scroll:el.scrollWidth})));
 assert.deepEqual(overflow,[],label);
}
try {
 await page.goto(origin);
 await page.getByText('3 queued · 2 uncertain · 1 failed').waitFor();
 await page.getByText('2026-10-24 09:01',{exact:true}).waitFor();
 await nav('Sites');
 await page.getByRole('button',{name:`Open site ${account.id}`,exact:true}).click();
 await page.getByRole('button',{name:'Add account',exact:true}).click();
 await page.getByRole('checkbox',{name:'Local number',exact:true}).check();
 await page.getByRole('button',{name:'Add 1 account',exact:true}).click();
 assert.deepEqual(writes.at(-1),{method:'PUT',path:`sites/${account.id}/channels`,body:{channels:[{channel:'WhatsApp',key:'local-number'}]}});
 await nav('Overview');
 const before = reads.filter(path=>path==='sites').length;
 await page.getByRole('button',{name:'Refresh',exact:true}).click();
 await page.waitForTimeout(150);
 assert.ok(reads.filter(path=>path==='sites').length > before);
 await nav('Connections');
 await page.getByRole('button',{name:'Open WhatsApp connection local-only-review',exact:true}).click();
 await page.getByText('+1555010100',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Close',exact:true}).last().click();
 await nav('Deliveries');
 await page.getByLabel('Account',{exact:true}).selectOption(account.id);
 await page.getByRole('button',{name:'Open delivery 41',exact:true}).click();
 await page.getByRole('button',{name:'Cancel queued send',exact:true}).click();
 assert.ok(!writes.some(row=>row.path==='delivery-queue'));
 await page.getByRole('button',{name:'Confirm cancellation',exact:true}).click();
 await page.getByText('No deliveries for this account.',{exact:true}).waitFor();
 await page.locator('.callback-row').click();
 await page.getByLabel('Recovery reason').fill('Local-only receipt reviewed');
 await page.getByRole('button',{name:'Confirm recovery',exact:true}).click();
 await page.getByRole('dialog').getByRole('alert').filter({hasText:'Callback remains quarantined'}).waitFor();
 callbackFail=false;
 await page.getByRole('button',{name:'Confirm recovery',exact:true}).click();
 await page.getByText('No failed callbacks for this account.',{exact:true}).waitFor();
 assert.deepEqual(writes.filter(row=>row.path==='callback-failures/retry').at(-1).body,{account:account.id,channel:'WhatsApp',id:'42',lane:'status',action:'replay',note:'Local-only receipt reviewed'});
 await nav('Events');
 await page.getByLabel('Account',{exact:true}).selectOption(account.id);
 await page.getByText('Local inbound message',{exact:true}).click();
 await page.locator('.event-payload').filter({hasText:'local_only'}).waitFor();
 await page.getByRole('button',{name:'Close',exact:true}).last().click();
 await page.getByRole('tab',{name:'System activity'}).click();
 await page.getByText('account.save',{exact:true}).waitFor();
 for(const width of [320,375,640,768,1440]) {
  await page.setViewportSize({width,height:900});
  for(const name of ['Overview','Containers','Sites','Connections','Deliveries','Events']) {
   await nav(name); await page.waitForTimeout(60); await noOverflow(`${name} at ${width}`);
   assert.equal(await page.getByRole('button',{name:/^Notifications/}).isVisible(),true);
  }
 }
 await page.setViewportSize({width:375,height:850});
 await nav('Containers'); await page.getByRole('button',{name:'Open container Local main',exact:true}).click();
 for(const tab of ['WhatsApp','Telegram','Email']) {
  await page.getByRole('tab',{name:new RegExp('^'+tab)}).click();
  await page.getByRole('button',{name:/^Add (WhatsApp number|Telegram bot|mailbox)$/}).click(); await noOverflow(`Add ${tab}`);
  await page.getByRole('button',{name:'Cancel',exact:true}).click();
 }
 await page.screenshot({path:process.cwd()+'/.runtime/v1-mobile.png'});
 const idleReads=reads.length; await page.waitForTimeout(1200); assert.equal(reads.length,idleReads,'No polling');
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({passed:true,fixtureAPIs:true,widths:[320,375,640,768,1440],siteAssignment:true,callbackReplayFailureAndSuccess:true,cancellationRefresh:true,noPolling:true,browserErrors:errors}));
} catch(error) { console.error({errors,body:(await page.locator('body').innerText()).slice(-5000)}); await page.screenshot({path:process.cwd()+'/.runtime/v1-failure.png'}); throw error; } finally {await browser.close();}
