// Theme (light/dark) persistence. Loaded via a non-deferred <script src>
// in <head> on every page so it runs synchronously before first paint —
// avoids a flash of the wrong theme on load. The toggle button only
// exists in the sidebar (layout.html); wireToggle() is a no-op on pages
// without one (login, change-password).
(function () {
  var STORAGE_KEY = 'doupro-theme';
  var stored = localStorage.getItem(STORAGE_KEY);
  var theme = stored === 'light' ? 'light' : 'dark';
  document.documentElement.dataset.theme = theme;

  function wireToggle() {
    var btn = document.getElementById('theme-toggle');
    if (!btn) return;
    var icon = document.getElementById('theme-toggle-icon');

    function render() {
      var current = document.documentElement.dataset.theme;
      icon.textContent = current === 'light' ? '☀️' : '🌙';
      btn.title = current === 'light' ? 'Switch to dark theme' : 'Switch to light theme';
    }
    render();

    btn.addEventListener('click', function () {
      var next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
      document.documentElement.dataset.theme = next;
      localStorage.setItem(STORAGE_KEY, next);
      render();
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireToggle);
  } else {
    wireToggle();
  }
})();
