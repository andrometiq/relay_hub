export default async function deliveryTest(page) {
 const p=await page.context().browser().newPage(), writes=[], errors=[];
 let fail=true, queued=true;
 p.on('pageerror',e=>errors.push(e.message));
 await p.route('**/hub/api/**',async route=>{
  const req=route.request(),path=req.url().split('/api/')[1];
  if(req.method()==='POST'){
   const data=req.postDataJSON();writes.push({data,csrf:req.headers()['x-relay-csrf']});
   if(data.action==='resolve'&&fail)return route.fulfill({status:503,body:'unavailable'});
   if(data.action==='cancel')queued=false;
   return route.fulfill({status:204});
  }
  let result={};
  if(path.startsWith('sites'))result={sites:[{id:'test-site',name:'Test',origin:'https://test.invalid',enabled:true,configured_secrets:[],channels:[{channel:'WhatsApp',key:'test-account',label:'Test line',enabled:true,config:{}}]}],next:'',csrf:'test-csrf'};
  if(path==='deliveries')result={counts:{queued:1,uncertain:1}};
  if(path.startsWith('delivery-queue'))result={deliveries:[...(queued?[{id:1,state:'queued'}]:[]),{id:2,state:'uncertain'}],more:false};
  return route.fulfill({json:result});
 });
 try{
  await p.goto('http://127.0.0.1:18126/hub/');
  await p.getByRole('button',{name:'Delivery',exact:true}).click();
  await p.getByLabel('Account',{exact:true}).selectOption('test-account');
  await p.getByRole('button',{name:'Cancel delivery',exact:true}).click();
  await p.getByRole('button',{name:'Cancel delivery',exact:true}).waitFor({state:'detached'});
  await p.getByRole('button',{name:'Resolve outcome',exact:true}).click();
  await p.getByRole('textbox',{name:'Verification evidence'}).fill('Verified in the provider dashboard.');
  await p.getByRole('textbox',{name:'Provider message ID (required except reactions)'}).fill('synthetic-id');
  await p.getByRole('button',{name:'Record resolution'}).click();
  await p.getByText('The request was not confirmed.',{exact:false}).waitFor();
  if(await p.getByRole('textbox',{name:'Verification evidence'}).inputValue()!=='Verified in the provider dashboard.')throw Error('Resolution draft lost');
  fail=false;
  await p.getByRole('button',{name:'Record resolution'}).click();
  await p.getByRole('button',{name:'Record resolution'}).waitFor({state:'detached'});
  for(const width of [320,390,768,1440]){
   await p.setViewportSize({width,height:900});
   if(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth))throw Error('Overflow '+width);
  }
  if(errors.length)throw Error(errors.join(';'));
  if(writes.length!==3||writes.some(w=>w.csrf!=='test-csrf'||w.data.account!=='test-account'))throw Error('Invalid delivery requests');
  return {passed:true,cancel:true,resolve:true,failurePreservesEvidence:true,csrf:true,widths:[320,390,768,1440]};
 }finally{await p.close();}
}
