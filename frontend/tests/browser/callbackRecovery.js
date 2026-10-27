// Installed Hub assets with API fixtures; no real callback is replayed.
async(page)=>{
 const ctx=await page.context().browser().newContext(),p=await ctx.newPage(),errors=[],writes=[];let fail=true,resolved=false;
 p.on('pageerror',e=>errors.push(e.message));
 try {
 await p.route('**/api/**',async route=>{
  const req=route.request(),path=req.url().split('/api/')[1];let result={};
  if(path==='callback-failures/retry'){
   writes.push(req.postDataJSON());if(fail)return route.fulfill({status:409,body:'Still quarantined'});resolved=true;return route.fulfill({status:204});
  }
  if(path.startsWith('accounts'))result={accounts:[{id:'fixture',site:'https://fixture.invalid',channels:[{channel:'Telegram',enabled:true}]}],csrf:'fixture',total:1,enabled_channels:1};
  if(path.startsWith('callback-failures?'))result={items:resolved?[]:[{id:42,reason:'payload_too_large',created_at:'2026-09-24'}],more:false};
  await route.fulfill({json:result});
 });
 await p.goto('http://127.0.0.1:18127/');
 await p.getByRole('button',{name:'Channels',exact:true}).click();
 await p.getByRole('button',{name:'Callback failures',exact:true}).click();
 await p.getByRole('button',{name:'Retry callback',exact:true}).click();
 await p.getByText('The event remains quarantined.',{exact:false}).waitFor();
 if(!await p.getByRole('button',{name:'Retry callback',exact:true}).isVisible())throw Error('Failure row lost');
 fail=false;await p.getByRole('button',{name:'Retry callback',exact:true}).click();
 await p.getByText('No quarantined events.',{exact:true}).waitFor();
 if(writes.length!==2||writes.some(w=>w.account!=='fixture'||w.channel!=='Telegram'||w.id!=='42'))throw Error('Wrong retry scope');
 if(errors.length)throw Error(JSON.stringify(errors));
 return {installedAssets:true,fixtureAPI:true,retryRequests:writes.length,failedRetryRetained:true,successfulRetryRefreshed:true,errors};
 }finally{await p.unrouteAll({behavior:'wait'});await ctx.close()}
}
