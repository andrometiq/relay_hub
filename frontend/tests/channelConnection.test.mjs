import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {ref,watch,reactive,nextTick,effectScope} from 'vue';
const source=(await readFile(new URL('../src/components/ChannelConnection.vue',import.meta.url),'utf8'))
 .split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'');
const make=new Function('ref','watch','onBeforeUnmount','defineProps','hubApi',source+'\nreturn {run,connection,ingress,busy,error};');

test('connection checks discard stale responses and clear ingress secrets on account changes',async()=>{
 const scope=effectScope();
 const props=reactive({account:'first',channel:'Email'}),calls=[];
 let finish,dispose;
 const pane=scope.run(()=>make(ref,watch,fn=>{dispose=fn},()=>props,(path,body)=>{calls.push({path,body});return new Promise(resolve=>{finish=resolve})}));
 const old=pane.run('ingress');
 assert.equal(pane.busy.value,true);
 props.account='second';await nextTick();
 finish({secret_token:'first-account-private'});await old;
 assert.equal(pane.ingress.value,null);
 const current=pane.run('ingress');finish({secret_token:'second-account-private'});await current;
 assert.equal(pane.ingress.value.secret_token,'second-account-private');
 props.channel='Telegram';await nextTick();
 assert.equal(pane.ingress.value,null);
 const repair=pane.run('repair');finish({matches:true});await repair;
 assert.deepEqual(calls.at(-1),{path:'accounts/second/telegram/connection',body:{action:'repair'}});
 const pending=pane.run('status');dispose();finish({matches:false});await pending;
 assert.equal(pane.connection.value,null);
 scope.stop();
});

test('connection errors stay visible and duplicate checks are suppressed',async()=>{
 const scope=effectScope();let reject,calls=0;
 const pane=scope.run(()=>make(ref,watch,()=>{},()=>({account:'local',channel:'Email'}),()=>{calls++;return new Promise((_,fail)=>{reject=fail})}));
 const pending=pane.run('test');await pane.run('test');assert.equal(calls,1);
 reject(Error('Connection unavailable'));await pending;
 assert.equal(pane.busy.value,false);assert.equal(pane.error.value,'Connection unavailable');
 scope.stop();
});
