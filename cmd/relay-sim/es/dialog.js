// Fake Embedded Signup dialog. It asks the simulation to create the assets,
// then posts WA_EMBEDDED_SIGNUP and the login result to the opener, as Meta does.
(function () {
  var params = new URLSearchParams(window.location.search);
  var target = openerOrigin(params.get('origin'));
  var extras = parseJSON(params.get('extras')) || {};
  var setup = extras.setup || {};
  var finished = false;
  var byId = function (id) { return document.getElementById(id); };

  function openerOrigin(value) {
    try {
      var url = new URL(value);
      return (url.protocol === 'http:' || url.protocol === 'https:') && url.origin === value ? value : '';
    } catch (error) {
      return '';
    }
  }

  function parseJSON(value) {
    try { return JSON.parse(value); } catch (error) { return null; }
  }

  function post(path, body) {
    body.app_id = params.get('app_id') || '';
    body.config_id = params.get('config_id') || '';
    return fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      .then(function (response) { return response.json().then(function (data) { return { ok: response.ok, data: data }; }); });
  }

  function showError(text) {
    var status = byId('status');
    status.textContent = text;
    status.hidden = !text;
  }

  function deliver(result) {
    finished = true;
    if (window.opener && target) {
      window.opener.postMessage(JSON.stringify({ type: 'WA_EMBEDDED_SIGNUP', event: result.event, data: result.data, version: '3' }), target);
      window.opener.postMessage({ type: 'SIM_FB_LOGIN', authResponse: result.code ? { code: result.code } : null }, target);
    }
    document.body.dataset.finished = result.event;
    setTimeout(function () { window.close(); }, 50);
  }

  function submit(body) {
    showError('');
    post('/es/dialog/finish', body).then(function (reply) {
      if (reply.ok && reply.data.event) deliver(reply.data);
      else showError(reply.data.error || 'Something went wrong.');
    }, function () { showError('The simulation is not reachable.'); });
  }

  function scenario() {
    var checked = document.querySelector('input[name=scenario]:checked');
    return checked ? checked.value : '';
  }

  function currentStep() {
    if (scenario() === 'coexistence') return 'WHATSAPP_BUSINESS_APP_QR';
    if (scenario() === 'new') return byId('otp-step').hidden ? 'PHONE_NUMBER_SETUP' : 'PHONE_NUMBER_VERIFICATION';
    return 'BUSINESS_PORTFOLIO_SELECTION';
  }

  function show(name) {
    document.querySelectorAll('section[data-scenario]').forEach(function (section) {
      section.hidden = section.dataset.scenario !== name;
    });
  }

  function drawQR(seed) {
    var svg = byId('qr');
    var cells = '';
    var value = 7;
    for (var i = 0; i < seed.length; i++) value = (value * 31 + seed.charCodeAt(i)) % 104729;
    for (var y = 0; y < 21; y++) {
      for (var x = 0; x < 21; x++) {
        var finder = (x < 7 && y < 7) || (x > 13 && y < 7) || (x < 7 && y > 13);
        value = (value * 1103 + 12345) % 104729;
        var on = finder ? (x % 6 === 0 || y % 6 === 0 || (x % 14 > 1 && x % 14 < 5 && y % 14 > 1 && y % 14 < 5)) : value % 2 === 0;
        if (on) cells += '<rect x="' + x + '" y="' + y + '" width="1" height="1"/>';
      }
    }
    svg.innerHTML = cells;
  }

  function wire() {
    var name = (setup.phone && setup.phone.displayName) || (setup.business && setup.business.name) || '';
    byId('new-display-name').value = name;
    byId('migrate-display-name').value = name;
    document.querySelectorAll('input[name=scenario]').forEach(function (input) {
      input.addEventListener('change', function () { show(input.value); });
    });
    var initial = extras.featureType === 'whatsapp_business_app_onboarding' ? 'coexistence' : 'new';
    document.querySelector('input[name=scenario][value=' + initial + ']').checked = true;
    show(initial);
    drawQR(params.get('config_id') || 'relay');

    byId('send-code').addEventListener('click', function () {
      byId('otp-step').hidden = false;
      byId('new-otp').focus();
    });
    byId('new-otp').addEventListener('input', function () {
      byId('finish-new').disabled = byId('new-otp').value.length !== 6;
    });
    byId('simulate-scan').addEventListener('click', function () {
      byId('simulate-scan').textContent = 'Scanned';
      byId('finish-coexistence').disabled = false;
    });
    byId('finish-new').addEventListener('click', function () {
      submit({ scenario: 'new', number: byId('new-number').value.trim(), otp: byId('new-otp').value.trim(), display_name: byId('new-display-name').value });
    });
    byId('finish-coexistence').addEventListener('click', function () {
      submit({ scenario: 'coexistence', number: byId('coexistence-number').value.trim(), share_history: byId('coexistence-history').checked });
    });
    byId('finish-migrate').addEventListener('click', function () {
      submit({ scenario: 'migrate', number: byId('migrate-number').value, display_name: byId('migrate-display-name').value });
    });
    byId('finish-cancel').addEventListener('click', function () {
      submit({ scenario: 'cancel', cancel_step: byId('cancel-step').value });
    });
    byId('finish-error').addEventListener('click', function () {
      submit({ scenario: 'error', error_message: byId('error-message').value });
    });
    fetch('/es/dialog/numbers').then(function (response) { return response.json(); }).then(function (data) {
      byId('migrate-number').innerHTML = '';
      data.numbers.forEach(function (number) {
        var option = document.createElement('option');
        option.value = number;
        option.textContent = '+' + number;
        byId('migrate-number').appendChild(option);
      });
    });
  }

  window.addEventListener('pagehide', function () {
    if (finished || !window.opener || !target) return;
    window.opener.postMessage(JSON.stringify({ type: 'WA_EMBEDDED_SIGNUP', event: 'CANCEL', data: { current_step: currentStep() }, version: '3' }), target);
    window.opener.postMessage({ type: 'SIM_FB_LOGIN', authResponse: null }, target);
  });

  document.addEventListener('DOMContentLoaded', function () {
    if (!target) showError('The opener origin is missing; events cannot be sent.');
    wire();
    post('/es/dialog/open', {}).then(function (reply) {
      if (reply.data.event) deliver(reply.data);
      else byId('scenarios').disabled = false;
    }, function () { showError('The simulation is not reachable.'); });
  });
})();
