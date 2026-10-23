import {test} from 'node:test'
import assert from 'node:assert/strict'
import {validateFields} from '../src/components/formValidation.js'
const fields=[{name:'site',label:'Site URL',required:true,url:true}]
test('accepts HTTP and HTTPS site origins with custom hosts and ports',()=>{
 for(const site of ['http://yrp.site:8002','http://localhost:8002','http://192.168.1.10:8002','https://erp.example.com','http://custom.example.com:8080/'])assert.deepEqual(validateFields(fields,{site}),{})
})
test('rejects malformed URLs, other protocols and non-origin URLs',()=>{
 for(const site of ['', 'yrp.site:8002','ftp://yrp.site','https://user:password@yrp.site','http://yrp.site/path','http://yrp.site?query=1','http://yrp.site#fragment','http://yrp.site:99999'])assert.ok(validateFields(fields,{site}).site,site)
})
