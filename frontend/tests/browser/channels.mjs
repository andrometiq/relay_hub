// Local built-UI fixtures only; never opens a configured site or provider.
import { createRequire } from 'node:module';
import assert from 'node:assert/strict';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const browser = await chromium.launch({ executablePath: '/opt/google/chrome/chrome', headless: true, args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
page.setDefaultTimeout(8000);
const writes = [], errors = []; let queueReads = 0, live;
page.on('pageerror', e => errors.push(e.message));
await page.routeWebSocket('**/socket.io/**', ws => {
  live = ws;
  ws.send('0{"sid":"local-engine","upgrades":[],"pingInterval":25000,"pingTimeout":20000,"maxPayload":1000000}');
  ws.onMessage(message => { if(message === '40') ws.send('40{"sid":"local-socket"}'); });
});
await page.route('**/api/**', async route => {
  const req = route.request(), url = new URL(req.url()), path = url.pathname.split('/api/')[1];
  if(req.method() === 'POST') { writes.push(req.postDataJSON()); return route.fulfill({status:204}); }
  let data = {};
  if(path === 'ready') data = {status:'ready'};
  if(path === 'accounts') data = {accounts:[{id:'local-only',site:'https://fixture.invalid',channels:[]}],total:1,csrf:'local-csrf'};
  if(path === 'deliveries') data = {counts:{uncertain:1}};
  if(path === 'delivery-queue') { queueReads++; data = {deliveries:[{id:9,state:'uncertain',operation:url.searchParams.get('channel') === 'Email' ? 'email.send' : 'telegram.message',attempts:1}],more:false}; }
  if(path === 'callback-failures') data = {items:[],more:false};
  await route.fulfill({json:data});
});
try {
  await page.goto('http://127.0.0.1:18130/');
  await page.getByRole('button',{name:'Deliveries',exact:true}).click();
  await page.getByLabel('Account',{exact:true}).selectOption('local-only');
  for(const channel of ['Telegram','Email']) {
    await page.getByRole('combobox',{name:'Channel',exact:true}).click();
    await page.getByRole('option',{name:channel,exact:true}).click();
    await page.getByRole('button',{name:'Open delivery 9',exact:true}).click();
    await page.getByRole('button',{name:'Resolve outcome',exact:true}).click();
    await page.getByLabel('Provider message ID',{exact:true}).fill(channel === 'Email' ? '<verified@example.invalid>' : '42');
    if(channel === 'Telegram') await page.getByLabel('Verified Telegram chat ID').fill('-10001');
    await page.getByLabel('Verification evidence').fill('Checked the local provider evidence.');
    await page.getByRole('button',{name:'Record outcome',exact:true}).click();
    await page.getByRole('dialog').waitFor({state:'detached'});
    assert.equal(writes.at(-1).channel,channel);
    if(channel === 'Telegram') assert.equal(writes.at(-1).chat_id,-10001);
  }
  const before = queueReads;
  live.send('42["hub:invalidate",{"account":"local-only"}]');
  await page.waitForResponse(response => response.url().includes('/api/delivery-queue'));
  assert.ok(queueReads > before);
  for(const width of [1440,390]) {
    await page.setViewportSize({width,height:1000});
    const overflow = await page.evaluate(() => [...document.querySelectorAll('.hub-shell,.hub-page')].some(el => el.scrollWidth > el.clientWidth + 1));
    assert.equal(overflow,false,`overflow at ${width}`);
    if(process.env.REVIEW_SCREENSHOTS) await page.screenshot({path:`${process.env.REVIEW_SCREENSHOTS}/channels-${width}.png`,fullPage:true});
  }
  assert.deepEqual(errors,[]);
  console.log('PASS: Telegram/email recovery, Socket.IO invalidation, desktop/mobile layout');
} finally { await browser.close(); }
