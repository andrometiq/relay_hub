import test from 'node:test';
import assert from 'node:assert/strict';
import {messagingLimit, qualityBadge, statusBadge, whatsappHealth} from '../src/composables/whatsappHealth.js';

test('quality ratings use their own colour and unknown is never green',()=>{
 assert.deepEqual(qualityBadge('GREEN'),{label:'High',theme:'green'});
 assert.deepEqual(qualityBadge('YELLOW'),{label:'Medium',theme:'orange'});
 assert.deepEqual(qualityBadge('RED'),{label:'Low',theme:'red'});
 assert.deepEqual(qualityBadge('UNKNOWN'),{label:'Unknown',theme:'gray'});
 assert.deepEqual(qualityBadge(undefined),{label:'Unknown',theme:'gray'});
});

test('statuses and limits read as plain text',()=>{
 assert.deepEqual(statusBadge('status','FLAGGED'),{label:'Flagged',theme:'orange'});
 assert.deepEqual(statusBadge('business_verification_status','verified'),{label:'Verified',theme:'green'});
 assert.deepEqual(statusBadge('name_status','NONE'),{label:'Unknown',theme:'gray'});
 assert.equal(messagingLimit('TIER_100K'),'100,000 customers a day');
 assert.equal(messagingLimit('TIER_250'),'250 customers a day');
 assert.equal(messagingLimit('TIER_UNLIMITED'),'Unlimited');
 assert.equal(messagingLimit(''),'Unknown');
});

test('health rows take the WABA values from the cached business profile',()=>{
 const rows=Object.fromEntries(whatsappHealth({quality_rating:'RED',throughput_level:'STANDARD',business:{account_review_status:'APPROVED',whatsapp_business_manager_messaging_limit:'TIER_2K'}}).map(row=>[row.name,row]));
 assert.equal(rows['Quality rating'].theme,'red');
 assert.equal(rows['Messaging limit'].text,'2,000 customers a day');
 assert.equal(rows['Account review'].label,'Approved');
 assert.equal(rows['Number status'].label,'Unknown');
 assert.match(rows.Throughput.text,/^Standard/);
});
