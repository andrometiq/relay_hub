// Fake Facebook JS SDK for the loopback simulation. It supports only the
// calls the Relay Hub signup page makes: FB.init, FB.login, FB.getLoginStatus.
(function () {
  var base = new URL(document.currentScript.src).origin;
  var settings = { appId: '', version: '' };
  var pending = null;

  function finish(response) {
    var callback = pending;
    pending = null;
    if (callback) callback(response);
  }

  window.addEventListener('message', function (event) {
    if (event.origin !== base) return;
    var data = event.data;
    if (typeof data === 'string') {
      try { data = JSON.parse(data); } catch (error) { return; }
    }
    if (!data || data.type !== 'SIM_FB_LOGIN') return;
    if (data.authResponse && data.authResponse.code) {
      finish({ status: 'connected', authResponse: { code: data.authResponse.code } });
    } else {
      finish({ status: 'unknown', authResponse: null });
    }
  });

  window.FB = {
    init: function (options) {
      settings.appId = String((options && options.appId) || '');
      settings.version = String((options && options.version) || '');
    },
    getLoginStatus: function (callback) {
      callback({ status: 'unknown', authResponse: null });
    },
    login: function (callback, options) {
      options = options || {};
      pending = callback;
      var query = new URLSearchParams({
        app_id: settings.appId,
        version: settings.version,
        config_id: String(options.config_id || ''),
        response_type: String(options.response_type || ''),
        extras: JSON.stringify(options.extras || {}),
        origin: window.location.origin
      });
      var dialog = window.open(base + '/es/dialog?' + query.toString(), 'sim-facebook-dialog', 'width=600,height=760');
      if (!dialog) finish({ status: 'unknown', authResponse: null });
    }
  };

  if (typeof window.fbAsyncInit === 'function') window.fbAsyncInit();
})();
