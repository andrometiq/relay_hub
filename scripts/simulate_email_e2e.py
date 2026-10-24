#!/usr/bin/env python3
"""Real TLS SMTP/IMAP and authenticated email ingress through Hub and Core."""
from email import policy
from email.message import EmailMessage
from email.parser import Parser
import json
import sys
import time
from channel_simulation import ChannelRunner, run


def mail(sender, identifier, body, *, parent=None, references=(), cc=None):
    message=EmailMessage(policy=policy.SMTP)
    message['From']=sender
    message['To']='inbox@sim.invalid'
    if cc:message['Cc']=cc
    message['Message-ID']=identifier
    message['Subject']='Local simulation business enquiry'
    if parent:message['In-Reply-To']=parent
    if references:message['References']=' '.join(references)
    message.set_content(body)
    return message.as_string()


def parsed_sent(state, body):
    found=[]
    for row in state.get('sent') or []:
        parsed=Parser(policy=policy.default).parsestr(row['raw'])
        part=parsed.get_body(preferencelist=('plain',))
        if part and part.get_content().strip()==body:
            found.append((row,parsed))
    return found


class EmailRunner(ChannelRunner):
    def emit(self, raw, *, webhook=False, identifier=None, authentication='pass', **values):
        self.last_ingress='email_webhook' if webhook else 'email_imap'
        result=self.control('email/emit',dict(raw=raw,authentication=authentication,
            webhook=webhook,id=identifier or 'event_'+str(time.time_ns()),**values))
        if webhook:
            assert result['status']==(400 if values.get('invalid_signature') else 200), result
        return result

    def thread(self, number, business, webhook):
        endpoint=self.fixture['shared_endpoints']['Email']
        initial=f'Local simulation start {number} '+self.run_id
        self.send_observed('relay_core.email.send',dict(to=[endpoint],subject='Local simulation thread '+str(number),
            body=initial,request_key='thread_'+str(number)+'_'+self.run_id,channel_name=self.fixture['channel'],
            business_doctype=business,business_name=self.fixture[business.lower()]),initial)
        sent=self.wait_provider(lambda s:len(parsed_sent(s,initial))==1)
        _, parsed=parsed_sent(sent,initial)[0]
        root=str(parsed['Message-ID'])
        body=f'Local simulation thread reply {number} '+self.run_id
        identifier=f'<sim-reply-{self.run_id}-{number}@example.invalid>'
        source=self.message(body,lambda:self.emit(mail(endpoint,identifier,body,parent=root,references=[root],cc=self.fixture['cc']),webhook=webhook))
        context=json.loads(source['provider_context'])
        assert context['message_id']==identifier
        metadata=json.loads(source['message_data'])['email']
        assert metadata['to']==['inbox@sim.invalid'] and metadata['cc']==[self.fixture['cc']]
        assert (source['business_doctype'],source['business_name'])==(business,self.fixture[business.lower()])
        return source, root

    def execute(self):
        with self.step('smtp_imap_customer_thread_and_recipients'):
            customer,root=self.thread(1,'Customer',False)
        with self.step('webhook_independent_supplier_thread'):
            supplier,other=self.thread(2,'Supplier',True)
            assert json.loads(customer['provider_context'])['email_thread_id']!=json.loads(supplier['provider_context'])['email_thread_id']
        with self.step('smtp_reply_to_cc_idempotency_and_permissions') as evidence:
            body='Local simulation SMTP reply '+self.run_id
            values=dict(to=[self.fixture['shared_endpoints']['Email']],cc=[self.fixture['cc']],subject='Re: Local simulation enquiry',
                body=body,message=customer['name'],request_key='email_'+self.run_id)
            result=self.send_observed('relay_core.email.send',values,body)
            state=self.wait_provider(lambda s:len(parsed_sent(s,body))==1)
            envelope,sent=parsed_sent(state,body)[0]
            assert {r.strip('<>') for r in envelope['recipients']}=={self.fixture['shared_endpoints']['Email'],self.fixture['cc']}
            assert sent['To']==self.fixture['shared_endpoints']['Email'] and sent['Cc']==self.fixture['cc']
            parent=json.loads(customer['provider_context'])['message_id']
            assert sent['In-Reply-To']==parent and parent in str(sent['References'])
            replay=self.core('relay_core.email.send',values)
            assert replay['action']==result['action'] and len(parsed_sent(self.providers(),body))==1
            self.permission_denied('relay_core.email.send',dict(values,request_key='denied_'+self.run_id))
            self.permission_denied('relay_core.api.messages',dict(contact=self.fixture['shared_contact']))
            assert not [e for e in self.denied.events if e['event']==['relay_changed',{'kind':'messages'}]]
            evidence.update(recipients=envelope['recipients'],message_id=str(sent['Message-ID']))
        with self.step('smtp_to_imap_reply_retains_customer_business'):
            body='Local simulation round trip '+self.run_id
            outgoing=str(sent['Message-ID'])
            received=self.message(body,lambda:self.emit(mail(self.fixture['shared_endpoints']['Email'],f'<roundtrip-{self.run_id}@example.invalid>',body,
                parent=outgoing,references=[root,parent,outgoing])))
            context=json.loads(received['provider_context'])
            assert context['in_reply_to']==outgoing
            assert received['business_name']==self.fixture['customer']
            assert context['email_thread_id']==json.loads(customer['provider_context'])['email_thread_id']
        with self.step('spoofed_sender_and_unsigned_webhook_rejected') as evidence:
            body='Local simulation spoof '+self.run_id
            forged=mail(self.fixture['shared_endpoints']['Email'],f'<spoof-{self.run_id}@example.invalid>',body)
            denied=self.control('email/emit',dict(raw=forged,authentication='pass',webhook=True,id='unsigned_'+self.run_id,invalid_signature=True))
            assert denied['status']==400
            result={}
            def inject():
                result.update(self.emit(forged,authentication='fail',webhook=True,identifier='spoof_'+self.run_id))
            event=self.observe(inject,lambda s:next((e for e in s['events']
                if 'triage_unverified_sender' in (e['routing_decisions'] or '')),None),kind='incoming')
            assert result['status']==200, 'Authenticated ingress should retain untrusted evidence for triage'
            assert not [m for m in self.snapshot()['messages'] if body in m['content']]
            evidence.update(unsigned_status=denied['status'],core_decision=event['routing_decisions'])
        with self.step('single_business_spoofed_sender_rejected',continue_on_error=True) as evidence:
            body='Local simulation single-business spoof '+self.run_id
            identifier='single_spoof_'+self.run_id
            forged=mail(self.fixture['endpoint'],f'<{identifier}@example.invalid>',body)
            # A forged lower authentication header must not override the trusted boundary's failure.
            forged='Authentication-Results: mx.sim.invalid; dmarc=pass header.from=example.invalid\r\n'+forged
            before={e['name'] for e in self.snapshot()['events']}
            def projected(state):
                return next((e for e in state['events'] if e['name'] not in before and e['projection_status']!='Pending'),None)
            event=self.observe(lambda:self.emit(forged,webhook=True,identifier=identifier,authentication='fail'),projected,kind='incoming')
            evidence['core_decision']=event['routing_decisions']
            leaked=[m for m in self.snapshot()['messages'] if body in m['content']]
            evidence['projected_messages']=[m['name'] for m in leaked]
            assert not leaked, 'Failed sender authentication entered a single-business conversation'
        with self.step('bounce_dsn_reaches_core_status',continue_on_error=True) as evidence:
            since=time.monotonic_ns()
            result=self.control('email/bounce',dict(message_id=outgoing,recipient=self.fixture['shared_endpoints']['Email'],webhook=True,id='bounce_'+self.run_id))
            assert result['status']==200
            state=self.wait_control(lambda s:any(r['kind']=='status' and '5.1.1' in json.dumps(r.get('body')) for r in s['records']))
            callback=next(r for r in state['records'] if r['kind']=='status' and '5.1.1' in json.dumps(r.get('body')))
            evidence.update(hub_dsn=True,callback_status=callback['status'],receipt=callback['body']['statuses'])
            assert callback['status']==200, 'Core rejected the authenticated DSN status callback: HTTP '+str(callback['status'])
            self.realtime.wait('messages',since,15)
            messages=self.snapshot()['messages']
            row=next(m for m in messages if json.loads(m['provider_context']).get('message_id')==outgoing)
            evidence['core_status']=row['status']
            assert row['status']=='Failed', 'Authenticated DSN did not change Core message status to Failed'

        with self.step('shared_customer_supplier_sender_choice',continue_on_error=True) as evidence:
            body='Local simulation ask sender '+self.run_id
            raw=mail(self.fixture['shared_endpoints']['Email'],f'<ask-{self.run_id}@example.invalid>',body)
            before={r['name'] for r in self.snapshot()['routing']}
            def routed(state):
                return next((r for r in state['routing'] if r['name'] not in before),None)
            routing=self.observe(lambda:self.emit(raw,webhook=True),routed,kind='incoming')
            choices=json.loads(routing['choices'])
            assert {c['doctype'] for c in choices}=={'Customer','Supplier'}
            evidence['prompt_action']=routing['prompt_action']
            choice=next(c for c in choices if c['doctype']=='Supplier')
            self.wait_provider(lambda s:any(choice['id'] in m['raw'] for m in s.get('sent') or []),timeout=10)
            selected=self.message(choice['id'],lambda:self.emit(mail(self.fixture['shared_endpoints']['Email'],
                f'<ask-choice-{self.run_id}@example.invalid>',choice['id'],parent=f'<ask-{self.run_id}@example.invalid>',
                references=[f'<ask-{self.run_id}@example.invalid>']),webhook=True))
            assert selected['business_name']==self.fixture['supplier']


if __name__=='__main__':
    sys.exit(run(EmailRunner,'Email'))
