async(page)=>{
 const ctx=await page.context().browser().newContext(),p=await ctx.newPage(),queries=[],errors=[];let writes=0;
 p.on('pageerror',e=>errors.push(e.message));
 try {
  await p.route('**/auth/**',async route=>{
   const req=route.request();if(!req.headers().accept?.includes('application/json'))return route.continue();
   if(req.method()!=='GET'){writes++;return route.fulfill({status:400,json:{Error:'Unexpected write'}})}
   if(req.url().includes('/auth/options?')){queries.push(decodeURIComponent(req.url().match(/[?&]query=([^&]*)/)?.[1]||''));return route.fulfill({json:[{value:'fixture-account',label:'தமிழ் account'}]})}
   return route.fulfill({json:{User:{role:'admin'},CSRF:'fixture',Scopes:['telegram.send','telegram.read'],Tokens:[],More:false}});
  });
  await p.goto('http://localhost:18081/auth/tokens');
  await p.getByRole('button',{name:'New token',exact:true}).click();
  const account=p.getByRole('combobox',{name:/Account/});await account.click();
  await p.getByRole('option',{name:/தமிழ் account/}).waitFor();
  await account.dispatchEvent('keydown',{key:'Enter',isComposing:true,bubbles:true});
  if(await account.getAttribute('aria-expanded')!=='true')throw Error('IME selected account');
  await account.evaluate(node=>{node.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));node.value='த';node.dispatchEvent(new InputEvent('input',{bubbles:true,isComposing:true}))});
  await p.waitForTimeout(400);if(queries.includes('த'))throw Error('Partial composition request');
  await account.evaluate(node=>{node.value='தமிழ்';node.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}))});
  await p.getByRole('option',{name:/தமிழ் account/}).waitFor();await account.press('Enter');
  await p.getByRole('button',{name:'Remove தமிழ் account',exact:true}).waitFor();
  const permissions=p.getByRole('combobox',{name:/Permissions/});await permissions.click();
  await p.getByRole('option',{name:'telegram.send',exact:true}).waitFor();
  await permissions.dispatchEvent('keydown',{key:'Enter',keyCode:229,bubbles:true});
  if(await p.getByRole('button',{name:'Remove telegram.send',exact:true}).count())throw Error('IME selected permission');
  await permissions.press('Enter');await p.getByRole('button',{name:'Remove telegram.send',exact:true}).waitFor();
  if(queries.filter(Boolean).join(',')!=='தமிழ்'||writes||errors.length)throw Error(JSON.stringify({queries,writes,errors}));
  return {installedAssets:true,apiFixtures:true,singleAndMultiSelect:true,compositionSearch:true,writes,errors};
 }finally{await p.unrouteAll({behavior:'wait'});await ctx.close()}
}
