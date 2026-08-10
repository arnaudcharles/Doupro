// Collapsible stacks on the Containers page. Expanded by default; the
// collapsed set is persisted per stack name so it survives reloads. A
// no-op on any page without [data-stack] sections.
(function () {
  var STORAGE_KEY = 'doupro-collapsed-stacks';

  function loadState() {
    try {
      return JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}');
    } catch (e) {
      return {};
    }
  }

  function saveState(state) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  }

  function wireStacks() {
    var state = loadState();

    document.querySelectorAll('[data-stack]').forEach(function (section) {
      var name = section.dataset.stack;
      var toggle = section.querySelector('.stack-toggle');
      var chevron = section.querySelector('.stack-chevron');
      var body = section.querySelector('.stack-body');
      if (!toggle || !body) return;

      function applyState(collapsed) {
        body.classList.toggle('hidden', collapsed);
        chevron.classList.toggle('rotate-90', !collapsed);
      }

      applyState(!!state[name]);

      toggle.addEventListener('click', function () {
        var collapsed = !state[name];
        state[name] = collapsed;
        saveState(state);
        applyState(collapsed);
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireStacks);
  } else {
    wireStacks();
  }
})();

// Show a donut progress spinner while update/rollback operations are running.
// Replaces the button with a spinner, makes an AJAX request, and reloads
// the page on success. This gives immediate visual feedback instead of
// the browser's silent redirect.
(function () {
  // Inject CSS for spinner animation if not already present
  if (!document.getElementById('doupro-spinner-style')) {
    var style = document.createElement('style');
    style.id = 'doupro-spinner-style';
    style.textContent = `
      @keyframes doupro-spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
      }
      .doupro-spinner {
        width: 1rem;
        height: 1rem;
        animation: doupro-spin 1s linear infinite;
        display: inline-block;
      }
    `;
    document.head.appendChild(style);
  }

  function createDonutSpinner() {
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'doupro-spinner');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');

    // Background circle (light gray, 15% opacity like other UI elements)
    var bgCircle = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
    bgCircle.setAttribute('cx', '12');
    bgCircle.setAttribute('cy', '12');
    bgCircle.setAttribute('r', '10');
    bgCircle.setAttribute('stroke', 'currentColor');
    bgCircle.setAttribute('stroke-width', '2');
    bgCircle.setAttribute('opacity', '0.15');

    // Progress arc (blue, matching badge-update color)
    var arc = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
    arc.setAttribute('cx', '12');
    arc.setAttribute('cy', '12');
    arc.setAttribute('r', '10');
    arc.setAttribute('fill', 'none');
    arc.setAttribute('stroke', 'currentColor');
    arc.setAttribute('stroke-width', '2');
    arc.setAttribute('stroke-dasharray', '62.8'); // circumference ≈ 62.8
    arc.setAttribute('stroke-dashoffset', '15.7'); // 1/4 of circumference, offset to show arc
    arc.setAttribute('stroke-linecap', 'round');

    svg.appendChild(bgCircle);
    svg.appendChild(arc);
    return svg;
  }

  function wireUpdateActions() {
    // Intercept clicks on Update/Rollback buttons instead of form submit,
    // since form.submit() bypasses submit event listeners.
    var buttons = document.querySelectorAll(
      'form[action*="/update"] button[type="submit"], form[action*="/rollback"] button[type="submit"]'
    );

    buttons.forEach(function (button) {
      button.addEventListener('click', function (e) {
        e.preventDefault();

        var form = button.closest('form');
        if (!form) return;

        var originalHTML = button.innerHTML;
        var originalClass = button.className;

        // Show spinner
        button.disabled = true;
        button.innerHTML = '';
        button.appendChild(createDonutSpinner());

        // AJAX request. "Accept: application/json" makes the server (see
        // api.respondAction/isJSON) return a plain JSON success/error body
        // instead of its plain-form-post fallback (a 303 redirect to "/" or
        // "/?error=..."). Without this header, fetch() silently follows the
        // redirect to a 200 response either way, making a real failure
        // (e.g. a Docker Hub pull rate-limit or a health-check timeout)
        // look identical to success - the spinner would finish and the page
        // would reload with nothing having actually changed, no error shown.
        fetch(form.action, {
          method: 'POST',
          headers: {
            'X-Requested-With': 'XMLHttpRequest',
            'Accept': 'application/json'
          }
        })
          .then(function (response) {
            return response.json().catch(function () { return {}; }).then(function (body) {
              return { ok: response.ok, body: body };
            });
          })
          .then(function (result) {
            if (!result.ok) {
              var message = (result.body && result.body.error && result.body.error.message) || 'operation failed';
              throw new Error(message);
            }
            // Reload page to show updated state
            setTimeout(function () {
              window.location.reload();
            }, 500); // Brief delay so user sees the spinner finish
          })
          .catch(function (error) {
            // Restore button on error
            button.disabled = false;
            button.innerHTML = originalHTML;
            button.className = originalClass;
            alert('Error: ' + error.message);
          });
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireUpdateActions);
  } else {
    wireUpdateActions();
  }
})();
