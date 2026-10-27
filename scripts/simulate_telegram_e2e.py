#!/usr/bin/env python3
"""Real Hub/Core Telegram acceptance against the local fake Bot API."""
from simulation_fixtures import run_number
import base64
import json
import sys
import time
import urllib.parse
from channel_simulation import ChannelRunner, run
from simulation_common import request


def update(chat, identifier, body, **fields):
    return dict(update_id=identifier, message=dict(message_id=identifier, date=int(time.time()),
        chat=dict(id=int(chat),type='private'), **{'from':dict(id=int(chat),first_name='Local simulation')},
        **({'text':body} if body else {}), **fields))


def accepted_text(state, text):
    return [m for m in state.get('messages') or [] if m['request'].get('text')==text]


class TelegramRunner(ChannelRunner):
    def emit(self, payload, **values):
        self.last_ingress='telegram_webhook'
        result=self.control('telegram/emit',dict(payload=payload,**values))
        assert result['status']==(400 if values.get('invalid_signature') else 200), result
        return result

    def send(self, source, text, key, user=None):
        return self.core('relay_core.messaging.send',dict(message=source['name'],kind='text',
            data=dict(text=text),request_key=key),user)

    def execute(self):
        chat=self.fixture['endpoint']
        identifier=run_number(self.run_id) * 2048
        with self.step('signed_ingress_and_deduplication') as evidence:
            first='Local simulation Telegram '+self.run_id
            initial=update(chat,identifier,first)
            self.emit(initial,invalid_signature=True)
            assert not self.snapshot()['messages']
            source=self.message(first,lambda:self.emit(initial))
            body='Local simulation routed '+self.run_id
            payload=update(chat,identifier+2,body)
            source=self.message(body,lambda:self.emit(payload))
            assert source['business_name']==self.fixture['customer']
            self.emit(payload)
            assert len([m for m in self.snapshot()['messages'] if m['content']==body])==1
            evidence.update(person=self.fixture['person'],business=source['business_doctype'])
        with self.step('core_send_and_permissions'):
            text='Local simulation outbound '+self.run_id
            sent=self.send(source,text,'normal_'+self.run_id)
            self.wait_provider(lambda s:len(accepted_text(s,text))==1)
            replay=self.send(source,text,'normal_'+self.run_id)
            assert replay['action']==sent['action']
            assert len(accepted_text(self.providers(),text))==1
            self.permission_denied('relay_core.messaging.send',dict(message=source['name'],kind='text',data={'text':text},request_key='denied_'+self.run_id))
            self.permission_denied('relay_core.api.messages',dict(contact=self.fixture['contact']))
            assert not [e for e in self.denied.events if e['event']==['relay_changed',{'kind':'messages'}]]
        with self.step('429_backoff') as evidence:
            text='Local simulation throttled '+self.run_id
            self.control('telegram/faults',dict(faults=['429']))
            self.send(source,text,'rate_'+self.run_id)
            self.wait_provider(lambda s:len(accepted_text(s,text))==1)
            records=[r for r in self.control('providers')['records'] if r['kind']=='telegram' and r.get('body',{}).get('text')==text]
            assert len(records)==2 and records[0]['status']==429 and records[1]['status']==200
            delay=(records[1]['started_ns']-records[0]['finished_ns'])/1e9
            assert delay>=.9, 'Retry ignored provider backoff'
            evidence.update(attempts=len(records),backoff_seconds=delay)
        with self.step('attachment_upload_send_ingress_download') as evidence:
            raw=b'Local simulation attachment: quantity 2, total 250.00\n'
            attachment=self.core('relay_core.attachments.upload',dict(message=source['name'],filename='Local simulation.txt',content_base64=base64.b64encode(raw).decode()))
            self.core('relay_core.messaging.send',dict(message=source['name'],kind='document',data=dict(attachment=attachment['name'],caption='Local simulation document'),request_key='media_'+self.run_id))
            provider=self.wait_provider(lambda s:any(m['method']=='sendDocument' for m in s.get('messages') or []))
            document=next(m['result']['document'] for m in provider['messages'] if m['method']=='sendDocument')
            caption='Local simulation incoming document '+self.run_id
            incoming=self.message(caption,lambda:self.emit(update(chat,identifier+3,None,document=document,caption=caption)))
            operator=self.fixture['operator']
            url=self.args.core_origin+'/api/method/relay_core.attachments.read?'+urllib.parse.urlencode(dict(message=incoming['name']))
            downloaded=request(url,headers={'Host':urllib.parse.urlsplit(self.canonical).netloc,
                'Authorization':f"token {operator['api_key']}:{operator['api_secret']}"},raw=True)
            assert downloaded==raw
            self.permission_denied('relay_core.attachments.read?'+urllib.parse.urlencode(dict(message=incoming['name'])),None)
            evidence.update(bytes=len(downloaded))
        with self.step('queued_send_restart_recovery'):
            text='Local simulation restart '+self.run_id
            self.control('telegram/faults',dict(faults=['429','429']))
            sent=self.send(source,text,'restart_'+self.run_id)
            self.wait_control(lambda s:any(d['state']=='queued' for d in s['channel_deliveries']))
            self.stop_hub();self.start(resume=True)
            self.wait_provider(lambda s:len(accepted_text(s,text))==1)
            replay=self.send(source,text,'restart_'+self.run_id)
            assert replay['action']==sent['action']
            assert len(accepted_text(self.providers(),text))==1

        with self.step('ambiguous_acceptance_and_restart_no_resend') as evidence:
            fault_body = 'Local simulation isolated uncertain destination ' + self.run_id
            fault_source = self.message(fault_body, lambda: self.emit(update(self.fixture['fault_endpoint'], identifier+8, fault_body)))
            text='Local simulation uncertain '+self.run_id
            self.control('telegram/faults',dict(faults=['disconnect_after']))
            result=self.send(fault_source,text,'uncertain_'+self.run_id)
            self.wait_control(lambda s:any(d['state']=='uncertain' for d in s['channel_deliveries']))
            assert len(accepted_text(self.providers(),text))==1
            self.stop_hub(); self.start(resume=True)
            replay=self.send(fault_source,text,'uncertain_'+self.run_id)
            assert replay['action']==result['action'] and replay['status']=='Uncertain', replay
            time.sleep(2)
            assert len(accepted_text(self.providers(),text))==1, 'Ambiguous delivery was resent'
            evidence.update(acceptances=1,action_status=replay['status'])
        with self.step('shared_customer_supplier_sender_choice', continue_on_error=True) as evidence:
            body='Local simulation shared choice '+self.run_id
            shared=self.fixture['shared_endpoints']['Telegram']
            def routed(state):
                return next((r for r in state['routing'] if r['relay_contact']==self.fixture['shared_contact']),None)
            routing=self.observe(lambda:self.emit(update(shared,identifier+10,body)),routed,kind='incoming')
            choices=json.loads(routing['choices'])
            assert {c['doctype'] for c in choices}=={'Customer','Supplier'}
            evidence.update(prompt_action=routing['prompt_action'],person=self.fixture['shared_person'])
            prompts=self.wait_provider(lambda s:any('Which business' in m['request'].get('text','') for m in s.get('messages') or []),timeout=10)
            prompt=next(m['request']['text'] for m in prompts['messages'] if 'Which business' in m['request'].get('text',''))
            assert 'relay-business:' not in prompt
            assert not routing['business_doctype'] and not routing['business_name'], 'Business was guessed before sender choice'
            for number, item in enumerate(choices, 1):
                assert f"{number}. {item['doctype']}: {item['title']}" in prompt
            choice=next(c for c in choices if c['doctype']=='Supplier')
            answer=str(choices.index(choice)+1)
            chosen=self.message('Chose: Supplier — '+choice['title'],lambda:self.emit(update(shared,identifier+11,answer)))
            assert chosen['business_name']==self.fixture['supplier']
            assert 'relay-business:' not in chosen['content']

        with self.step('single_inbound_latency', continue_on_error=True) as evidence:
            evidence.update(self.latency_acceptance())
        with self.step('supplier_ai_scoped_ledger_and_handoff', continue_on_error=True) as evidence:
            evidence.update(self.channel_ai_acceptance())
        with self.step('sales_order_event_and_hub_outage', continue_on_error=True) as evidence:
            evidence.update(self.channel_business_acceptance())


if __name__=='__main__':
    sys.exit(run(TelegramRunner,'Telegram'))
