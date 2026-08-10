// Sidebar collapse persistence. Loaded via a non-deferred <script src> in
// <head> on every page, same as theme.js, so [data-sidebar] is set on
// <html> before first paint — see the matching CSS rules in
// web/input.css for why that avoids a flash of the sidebar being shown
// then immediately hidden. wireToggle() is a no-op on pages without a
// sidebar (login, change-password).
(function () {
  var STORAGE_KEY = 'doupro-sidebar-collapsed';
  var collapsed = localStorage.getItem(STORAGE_KEY) === '1';
  document.documentElement.dataset.sidebar = collapsed ? 'collapsed' : 'expanded';

  function setCollapsed(next) {
    collapsed = next;
    document.documentElement.dataset.sidebar = collapsed ? 'collapsed' : 'expanded';
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0');
  }

  function wireToggle() {
    var hideBtn = document.getElementById('sidebar-hide-btn');
    var showBtn = document.getElementById('sidebar-show-btn');
    if (!hideBtn || !showBtn) return;
    hideBtn.addEventListener('click', function () { setCollapsed(true); });
    showBtn.addEventListener('click', function () { setCollapsed(false); });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireToggle);
  } else {
    wireToggle();
  }
})();
