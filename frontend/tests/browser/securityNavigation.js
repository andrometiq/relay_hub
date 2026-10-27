async(page)=>{
 const ctx=await page.context().browser().newContext({viewport:{width:1280,height:900}}),p=await ctx.newPage(),errors=[];let writes=0;
 p.on('pageerror',e=>errors.push(e.message));
 try {
  await p.route('**/auth/**',async r=>{
   if(!r.request().headers().accept?.includes('application/json'))return r.continue();
   if(r.request().method()!=='GET')writes++;
   await r.fulfill({json:{User:{role:'admin'},CSRF:'fixture',Tokens:[],Sessions:[],Users:[],More:false}});
  });
  await p.goto('http://localhost:18081/auth/tokens');await p.getByRole('button',{name:'New token',exact:true}).waitFor();
  const nav=p.getByRole('complementary',{name:'Account navigation'});
  const collapsed=await nav.boundingBox(),main=await p.locator('main').boundingBox();
  await nav.hover();await p.waitForTimeout(220);const expanded=await nav.boundingBox(),after=await p.locator('main').boundingBox();
  if(Math.round(collapsed.width)!==54||Math.round(expanded.width)!==220||main.x!==after.x||main.width!==after.width)throw Error('Sidebar resize/layout shift');
  await nav.getByRole('link',{name:'Sessions',exact:true}).click();
  await p.waitForURL('**/auth/sessions');await nav.getByRole('link',{name:'Sessions',exact:true}).waitFor();
  if(await nav.getByRole('link',{name:'Sessions',exact:true}).getAttribute('aria-current')!=='page')throw Error('Missing active navigation');
  await p.goBack();await p.getByRole('button',{name:'New token',exact:true}).waitFor();
  await p.mouse.move(1200,400);
  await nav.getByRole('link',{name:'Relay Hub home',exact:true}).focus();await p.keyboard.press('Tab');await p.waitForTimeout(220);
  if(Math.round((await nav.boundingBox()).width)!==220)throw Error('Keyboard focus did not expand sidebar');
  const colors=[];
  for(const theme of ['light','dark']){
   await p.evaluate(value=>{localStorage.setItem('relay-theme',value);window.dispatchEvent(new StorageEvent('storage',{key:'relay-theme',newValue:value}))},theme);
   colors.push(await nav.evaluate(el=>getComputedStyle(el).backgroundColor));
  }
  if(colors[0]===colors[1])throw Error('Sidebar does not follow theme');
  await p.setViewportSize({width:390,height:844});
  if(await nav.isVisible())throw Error('Mobile sidebar should start closed');
  await p.getByRole('button',{name:'Toggle navigation',exact:true}).click();await nav.waitFor({state:'visible'});
  await nav.getByRole('link',{name:'Sessions',exact:true}).click();
  await nav.waitFor({state:'hidden'});
  if(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth)||errors.length||writes)throw Error(JSON.stringify({errors,writes}));
  return {installedAssets:true,apiFixtures:true,collapsed:collapsed.width,expanded:expanded.width,noContentShift:true,keyboardExpansion:true,themeColors:colors,mobile:true,backNavigation:true,writes,errors};
 }finally{await p.unrouteAll({behavior:'wait'});await ctx.close()}
}
