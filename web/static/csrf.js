// CSRF protection for session-cookie-authenticated requests (see
// internal/auth/middleware.go's double-submit check). Loaded on every
// page so every fetch() call and every plain <form method="post"> submit
// automatically carries the doupro_csrf cookie's value back to the
// server — no template or call-site changes needed elsewhere.
(function () {
  function getCookie(name) {
    var m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  }

  var originalFetch = window.fetch;
  window.fetch = function (input, init) {
    init = init || {};
    var method = (init.method || 'GET').toUpperCase();
    if (method !== 'GET' && method !== 'HEAD') {
      init.headers = new Headers(init.headers || {});
      init.headers.set('X-CSRF-Token', getCookie('doupro_csrf'));
    }
    return originalFetch(input, init);
  };

  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (form.tagName !== 'FORM' || form.method.toUpperCase() !== 'POST') return;
    var field = form.querySelector('input[name="csrf_token"]');
    if (!field) {
      field = document.createElement('input');
      field.type = 'hidden';
      field.name = 'csrf_token';
      form.appendChild(field);
    }
    field.value = getCookie('doupro_csrf');
  }, true);
})();
