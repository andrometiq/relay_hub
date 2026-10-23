export default async function hubWorkflows(page) {
 const p=await page.context().browser().newPage(), errors=[],writes=[],reads=[];
 p.on('pageerror',e=>errors.push(e.message));
 let fail=false;
 const account={id:'review-account',site:'https://erp.example.test',enabled:true,channels:[{channel:'Telegram',enabled:true,config:{},configured_secrets:['secret']}]};
 await p.route('**/hub/api/**',async route=>{
  const req=route.request(),path=req.url().split('/api/')[1],data=req.method()==='POST'?req.postDataJSON():null; reads.push(path);
  if(data!==null){writes.push({path,data,csrf:req.headers()['x-relay-csrf']});if(fail)return route.fulfill({status:503,body:'unavailable'});return route.fulfill({status:204});}
  let result={};if(path.startsWith('accounts'))result={accounts:[account],csrf:'synthetic-csrf',total:65,next:path.includes('after=review-account')?'':'review-account',enabled_channels:1};
  if(path==='channels')result={whatsapp:0,telegram:1,email:0};if(path==='deliveries')result={counts:{queued:3}};if(path==='audit')result={events:[],more:false};
  return route.fulfill({json:result});
 });
 try {
  await p.goto('http://127.0.0.1:18126/hub/');
  await p.getByRole('button',{name:'Accounts',exact:true}).click();
  await p.getByRole('button',{name:'Edit review-account',exact:true}).click();
  if(!await p.getByRole('textbox',{name:/^Account ID/}).isDisabled())throw Error('Existing ID editable');
  await p.getByRole('textbox',{name:/^Site URL/}).fill('https://changed.example.test');
  fail=true;await p.getByRole('button',{name:'Save',exact:true}).click();
  await p.getByRole('status').filter({hasText:'not confirmed'}).waitFor();
  if(await p.getByRole('textbox',{name:/^Site URL/}).inputValue()!=='https://changed.example.test')throw Error('Draft lost');
  fail=false;await p.getByRole('button',{name:'Save',exact:true}).click();
  await p.getByRole('status').filter({hasText:'runtime reloaded'}).waitFor();
  await p.getByRole('button',{name:'Next accounts',exact:true}).click();
  await p.getByText('Page 2',{exact:false}).waitFor();
  await p.getByRole('button',{name:'Previous accounts',exact:true}).click();
  await p.getByLabel('Search accounts',{exact:true}).fill('review');
  await p.getByRole('button',{name:'Search accounts',exact:true}).click();
  await p.waitForResponse(r=>r.url().includes('query=review'));
  await p.getByRole('button',{name:'New account',exact:true}).click();
  await p.getByRole('textbox',{name:/^Account ID/}).fill('new-account');
  await p.getByRole('textbox',{name:/^Site URL/}).fill('http://localhost:8002');
  await p.getByRole('button',{name:'Channels',exact:true}).last().click();
  await p.getByRole('option',{name:'Telegram',exact:true}).click();
  await p.getByRole('heading',{name:'New account',exact:true}).click();
  if(!await p.getByRole('checkbox',{name:'Enable channel',exact:true}).isChecked())throw Error('New channel disabled');
  await p.getByRole('button',{name:'Save',exact:true}).click();
  await p.getByRole('status').filter({hasText:'runtime reloaded'}).waitFor();
  for(const width of [320,390,768,1440]){await p.setViewportSize({width,height:900});if(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth))throw Error('overflow '+width);}
  if(errors.length)throw Error(errors.join(';'));
  if(writes.some(w=>w.csrf!=='synthetic-csrf'))throw Error('CSRF missing');
  if(!writes.some(w=>w.data.create===true))throw Error('Create-only guard missing');
  return {passed:true,saveFailurePreservesDraft:true,newChannelEnabled:true,createOnly:true,csrf:true,serverPagination:true,search:true,widths:[320,390,768,1440]};
 } finally {await p.close();}
}
