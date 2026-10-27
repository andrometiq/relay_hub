// Local API fixtures, built frontend. No real users, tokens or sessions are changed.
import {createRequire} from 'node:module';
import {readFile} from 'node:fs/promises';
import assert from 'node:assert/strict';
const {chromium}=createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE);
const browser=await chromium.launch({executablePath:'/opt/google/chrome/chrome',headless:true,args:['--no-sandbox']});
const page=await browser.newPage({viewport:{width:375,height:900}});
page.setDefaultTimeout(8000);
const errors=[];
page.on('pageerror',e=>errors.push(e.message));
page.on('console',message=>{if(message.type()==='error'&&!message.text().includes('Failed to load resource'))errors.push(message.text())});
const base={User:{id:1,role:'admin'},CSRF:'local-only',More:false};
const users=Array.from({length:350},(_,i)=>({id:i+2,username:`local-only-user-${i}${i%3===1?'-long-name'.repeat(12):''}`,role:'member',enabled:true,grants:['local-active','local-disabled'],effective_grants:['local-active']}));
let rejectMutation=true,failGrantRead=false,pausePage;
const grantReads=[];
await page.route('**/auth/**',async route=>{
 const req=route.request(),url=new URL(req.url()),view=url.pathname.split('/').at(-1);
 if(req.isNavigationRequest()){
  const html=(await readFile(new URL('../../dist/index.html',import.meta.url),'utf8')).replace('<head>','<head><base href="/">');
  return route.fulfill({contentType:'text/html',body:html});
 }
 if(req.method()==='POST'){
  if(view==='users'&&!rejectMutation){
   const values=new URLSearchParams(req.postData()),user=users.find(row=>row.id===Number(values.get('id')));
   user.enabled=values.get('enabled')==='true';
   user.effective_grants=user.enabled?['local-active']:[];
   return route.fulfill({json:{...base,Notice:'Saved'}});
  }
  return route.fulfill({status:409,json:{Error:'Local action refused; current state changed'}});
 }
 let data={...base};
 if(view==='users'){
  const cursor=Number(url.searchParams.get('cursor')),before=url.searchParams.get('direction')==='before';
  if(cursor===1){
   grantReads.push(cursor);
   if(failGrantRead)return route.fulfill({status:503,json:{Error:'Local grant lookup unavailable'}});
  }
  const matches=users.filter(row=>(!cursor||(before?row.id<cursor:row.id>cursor))&&row.username.includes(url.searchParams.get('q')||''));
  data.Users=before?matches.slice(-50):matches.slice(0,50);
  data.More=matches.length>50;
  if(cursor&&pausePage)await new Promise(release=>{const resolve=pausePage;pausePage=null;resolve({release,cursor,before});});
 }
 if(view==='tokens'){data.Scopes=['messages:send'];data.Tokens=[{ID:1,Name:'local-only-key',Account:'local-active',Expires:'2020-01-01 00:00:00+00',Scopes:'messages:send',Revoked:false}];}
 if(view==='sessions')data.Sessions=[{ID:1,Current:true,Agent:'Local mobile browser',Seen:'2026-10-24'}];
 return route.fulfill({json:data});
});
async function noOverflow(){assert.equal(await page.evaluate(()=>document.querySelector('.hub-page').scrollWidth>document.querySelector('.hub-page').clientWidth),false);}
async function nav(name){await page.getByRole('button',{name:'Toggle navigation'}).click();await page.getByRole('link',{name,exact:true}).click();}
async function checkPaging(width){
 await page.setViewportSize({width,height:900});
 await page.goto('http://127.0.0.1:18129/auth/users');
 const viewport=page.locator(width<=640?'.infinite-list > .overflow-auto':'.infinite-list [aria-label="Records"]');
 await viewport.locator('[data-row-id="2"]').waitFor();
 await page.locator('.infinite-list__status').waitFor({state:'hidden'});
 if(width<=640){
  const heights=await viewport.locator('[data-row-id]').evaluateAll(rows=>rows.map(row=>row.getBoundingClientRect().height));
  assert.ok(Math.min(...heights)>=112);
  assert.ok(new Set(heights).size>1,'Mobile fixture must have variable card heights');
 }
 for(const before of [false,true])for(let step=0;step<(before?3:6);step++){
  const requested=new Promise((resolve,reject)=>{
   const timeout=setTimeout(()=>reject(new Error('Pagination request did not arrive')),8000);
   pausePage=request=>{clearTimeout(timeout);resolve(request);};
  });
  await viewport.evaluate((el,before)=>{el.scrollTop=before?80:el.scrollHeight-el.clientHeight-80;},before);
  const request=await requested;
  assert.equal(request.before,before);
  assert.equal(request.cursor,before?152-step*50:51+step*50);
  // Keep scrolling while the response is pending: the anchor must be measured
  // when the records change, not when the request started.
  await viewport.evaluate((el,before)=>{el.scrollTop+=before?35:-35;},before);
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  const anchor=await viewport.evaluate(el=>{
   const top=el.getBoundingClientRect().top;
   const row=[...el.querySelectorAll('[data-row-id]')].find(row=>row.getBoundingClientRect().bottom>top&&row.getBoundingClientRect().top<top+el.clientHeight);
   return {id:row.dataset.rowId,top:row.getBoundingClientRect().top-top};
  });
  request.release();
  await page.locator('.infinite-list__status').waitFor({state:'hidden'});
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  const top=await viewport.evaluate((el,id)=>{
   const row=el.querySelector(`[data-row-id="${id}"]`);
   return row?row.getBoundingClientRect().top-el.getBoundingClientRect().top:null;
  },anchor.id);
  assert.notEqual(top,null,'Visible record must remain rendered');
  assert.ok(Math.abs(top-anchor.top)<=1,`${width}px ${before?'prepend':'append'} page ${step}: anchor moved ${top-anchor.top}px`);
  if(width<=640)assert.equal(await viewport.locator('[data-row-id]').count(),before?200:Math.min(100+step*50,200));
 }
 await noOverflow();
}
try{
 await page.goto('http://127.0.0.1:18129/auth/users');
 await page.getByRole('button').filter({hasText:'local-only-user-0'}).click();
 await page.getByText('local-active · Site access enabled',{exact:true}).waitFor();
 await page.getByText('local-disabled · User or account disabled',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Disable',exact:true}).click();
 await page.getByRole('dialog').getByRole('alert').filter({hasText:'Local action refused'}).waitFor();
 rejectMutation=false;
 await page.getByRole('button',{name:'Disable',exact:true}).click();
 await page.getByText('local-active · User or account disabled',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Enable',exact:true}).click();
 await page.getByText('local-active · Site access enabled',{exact:true}).waitFor();
 await page.getByText('local-disabled · User or account disabled',{exact:true}).waitFor();
 assert.equal(grantReads.length,2,'Both status mutations must reload the selected user');
 failGrantRead=true;
 await page.getByRole('button',{name:'Disable',exact:true}).click();
 await page.getByRole('dialog').getByRole('alert').filter({hasText:'User status saved, but site access could not be refreshed: Local grant lookup unavailable'}).waitFor();
 await page.getByText('local-active · Site access not refreshed',{exact:true}).waitFor();
 failGrantRead=false;
 await page.getByRole('button',{name:'Enable',exact:true}).click();
 await page.getByText('local-active · Site access enabled',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Close',exact:true}).click();
 await noOverflow();
 await page.locator('.infinite-list > .overflow-auto').evaluate(el=>el.scrollTop=el.scrollHeight);
 await page.locator('[data-row-id="51"]').waitFor();
 await nav('Auth keys');
 await page.getByText('Expired',{exact:true}).waitFor();
 await page.getByRole('button').filter({hasText:'local-only-key'}).click();
 await page.getByRole('button',{name:'Revoke key',exact:true}).click();
 await page.getByRole('dialog').getByRole('alert').filter({hasText:'Local action refused'}).waitFor();
 await page.getByRole('button',{name:'Close',exact:true}).last().click();
 await nav('Sessions');
 await page.getByText('Local mobile browser',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Revoke',exact:true}).click();
 await page.getByRole('alert').filter({hasText:'Local action refused'}).waitFor();
 await noOverflow();
 await nav('Password');
 await page.getByRole('heading',{name:'Change password'}).waitFor();
 await noOverflow();
 await page.emulateMedia({colorScheme:'dark',reducedMotion:'reduce'});
 await page.screenshot({path:process.cwd()+'/.runtime/v1-access-dark.png'});
 for(const width of [375,640,1440])await checkPaging(width);
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({passed:true,fixtureAPIs:true,effectiveGrants:true,grantRefreshAndFailure:true,actionErrors:true,expiredKeys:true,sessionsNavigation:true,paginationRecords:350,bidirectionalAnchoringWidths:[375,640,1440],darkAndReducedMotion:true,browserErrors:errors}));
}catch(error){console.error({errors,body:await page.locator('body').innerText()});throw error;}finally{await browser.close();}
