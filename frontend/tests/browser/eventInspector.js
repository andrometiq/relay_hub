// Built Hub UI with synthetic API data; no provider traffic.
async(page)=>{
 const ctx=await page.context().browser().newContext(),p=await ctx.newPage(),errors=[];p.on('pageerror',e=>errors.push(e.message));
 try{
 await p.route('**/api/**',async route=>{
  const path=route.request().url().split('/api/')[1];let result={};
  if(path.startsWith('accounts'))result={accounts:[{id:'fixture',site:'https://fixture.invalid',channels:[]}],csrf:'fixture',total:1};
  if(path.startsWith('event-inspector?')){
   if(path.includes('id=51'))result={raw:JSON.stringify({text:'<script>window.injected=true</script>',message_id:123}),truncated:false};
   else if(path.includes('channel=WhatsApp'))result={items:[],more:false};
   else result={items:[{id:path.includes('before=')?1:51,time:'2026-09-24',kind:'inbound',preview:'Sample message',callback:'Forwarded'}],more:!path.includes('before=')};
  }
  await route.fulfill({json:result});
 });
 await p.goto('http://127.0.0.1:18127/');await p.getByRole('button',{name:'Events',exact:true}).click();
 await p.getByRole('combobox',{name:'Account',exact:true}).selectOption('fixture');
 await p.getByRole('button',{name:'Inspect event 51',exact:true}).click();await p.locator('pre').waitFor();
 if(!(await p.locator('pre').innerText()).includes('message_id'))throw Error('Payload missing');
 if(await p.evaluate(()=>window.injected))throw Error('Payload executed');
 await p.getByRole('button',{name:'Close event',exact:true}).click();
 if(await p.getByRole('region',{name:'Event payload'}).count())throw Error('Close failed');
 await p.getByRole('button',{name:'Older events',exact:true}).click();await p.getByRole('button',{name:'Inspect event 1',exact:true}).waitFor();
 await p.getByRole('button',{name:'Newer events',exact:true}).click();await p.getByRole('button',{name:'Inspect event 51',exact:true}).click();await p.locator('pre').waitFor();
 await p.getByRole('combobox',{name:'Event channel',exact:true}).selectOption('WhatsApp');await p.getByText('No events for this account and channel.',{exact:true}).waitFor();
 if(await p.locator('pre').count())throw Error('Stale channel detail');
 await p.getByRole('combobox',{name:'Event channel',exact:true}).selectOption('Telegram');await p.getByRole('button',{name:'Inspect event 51',exact:true}).click();await p.locator('pre').waitFor();
 const layouts=[];
 for(const theme of ['light','dark'])for(const width of [1280,390]){
  await p.setViewportSize({width,height:850});await p.evaluate(theme=>{document.documentElement.setAttribute('data-theme',theme);document.documentElement.classList.toggle('dark',theme==='dark')},theme);
  const overflow=await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);if(overflow)throw Error('Page overflow '+theme+' '+width);layouts.push({theme,width});
 }
 if(errors.length)throw Error(JSON.stringify(errors));return {pagination:true,escapedPayload:true,scopeReset:true,close:true,layouts,errors};
 }finally{await ctx.close()}
}
