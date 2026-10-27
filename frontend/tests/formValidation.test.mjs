import {test} from 'node:test'
import assert from 'node:assert/strict'
import {validateFields} from '../src/components/formValidation.js'
const fields=[{name:'site',label:'Site URL',required:true,url:true}]
test('accepts HTTP and HTTPS site origins with custom hosts and ports',()=>{
 for(const site of ['http://acme.localhost:8002','http://localhost:8002','http://192.168.1.10:8002','https://erp.example.com','http://custom.example.com:8080/'])assert.deepEqual(validateFields(fields,{site}),{})
})
test('rejects malformed URLs, other protocols and non-origin URLs',()=>{
 for(const site of ['', 'acme.localhost:8002','ftp://acme.localhost','https://user:password@acme.localhost','http://acme.localhost/path','http://acme.localhost?query=1','http://acme.localhost#fragment','http://acme.localhost:99999'])assert.ok(validateFields(fields,{site}).site,site)
})
