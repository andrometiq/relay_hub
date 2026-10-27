export default async function loginTest(page, base = 'http://localhost:18081') {
 const p = await page.context().browser().newPage();
 const errors = [], submissions = [];
 p.on('pageerror', error => errors.push(error.message));
 await p.route('**/auth/login', async route => {
  const request = route.request();
  if (request.method() === 'POST') {
   submissions.push(request.postData());
   return route.fulfill({status:401, json:{Error:'Synthetic credentials rejected'}});
  }
  if (request.headers().accept?.includes('application/json'))
   return route.fulfill({json:{CSRF:'synthetic-login-challenge'}});
  return route.continue();
 });
 try {
  await p.goto(base + '/auth/login');
  await p.getByRole('button', {name:'Sign in',exact:true}).click();
  if (submissions.length) throw Error('Empty form submitted');
  await p.getByRole('textbox', {name:/Username/}).fill('synthetic-user');
  await p.locator('input[type=password]').fill('synthetic-password');
  await p.getByRole('button', {name:'Sign in',exact:true}).click();
  await p.getByText('Synthetic credentials rejected',{exact:true}).waitFor();
  if (errors.length) throw Error(errors.join('; '));
  if (submissions.length!==1 || !submissions[0].includes('csrf=synthetic-login-challenge')) throw Error('Submission missing CSRF');
  if (await p.getByRole('textbox',{name:/Username/}).inputValue()!=='synthetic-user') throw Error('Failed login cleared username');
  return {passed:true,requiredValidation:true,submission:true,csrf:true,errorShown:true,noRuntimeErrors:true};
 } finally {await p.close();}
}
