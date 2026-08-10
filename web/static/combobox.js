// A small, dependency-free searchable dropdown ("combobox"), styled to
// match the app's dark theme instead of relying on <datalist> — which
// renders an inconsistent native arrow/popup across browsers that clashes
// with the custom input styling everywhere else in the UI.
//
// Single-select: picking an option fills the input's value directly (no
// hidden field needed — the input itself is what gets submitted).
// Multi-select: the input is search-only; picking an option adds it as a
// removable chip and appends it to a hidden CSV input, for fields like
// Settings' exclusions where more than one value can be selected.
function attachCombobox(input, { getOptions, onSelect, multi = false, chipsEl, hiddenEl } = {}) {
  const panel = document.createElement('div');
  panel.className = 'absolute z-10 mt-1 w-full max-h-48 overflow-auto rounded-md border border-gray-800 bg-gray-900 shadow-lg text-sm hidden';
  input.parentElement.style.position = 'relative';
  input.parentElement.appendChild(panel);

  let currentOptions = [];
  let highlighted = -1;

  const escapeHtml = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

  function selected() {
    if (!multi) return [];
    return hiddenEl.value.split(',').map((s) => s.trim()).filter(Boolean);
  }

  function renderChips() {
    if (!multi) return;
    const sel = selected();
    chipsEl.innerHTML = sel.map((name) => `
      <span class="inline-flex items-center gap-1 text-xs px-2 py-1 rounded bg-gray-800 text-gray-200">
        ${escapeHtml(name)}
        <button type="button" data-name="${escapeHtml(name)}" class="combobox-chip-remove text-gray-500 hover:text-gray-200">&times;</button>
      </span>
    `).join('');
    chipsEl.querySelectorAll('.combobox-chip-remove').forEach((btn) => {
      btn.addEventListener('click', () => {
        hiddenEl.value = sel.filter((n) => n !== btn.dataset.name).join(',');
        renderChips();
      });
    });
  }

  function highlight() {
    panel.querySelectorAll('.combobox-option').forEach((el, i) => {
      el.classList.toggle('bg-gray-800', i === highlighted);
    });
  }

  function open() {
    const query = input.value.trim().toLowerCase();
    const sel = selected();
    currentOptions = getOptions()
      .filter((o) => !sel.includes(o) && o.toLowerCase().includes(query))
      .slice(0, 8);
    highlighted = -1;
    if (currentOptions.length === 0) {
      panel.classList.add('hidden');
      return;
    }
    panel.innerHTML = currentOptions
      .map((o, i) => `<button type="button" data-index="${i}" class="combobox-option block w-full text-left px-2 py-1.5 hover:bg-gray-800 text-gray-200">${escapeHtml(o)}</button>`)
      .join('');
    panel.querySelectorAll('.combobox-option').forEach((btn) => {
      // mousedown (not click) fires before the input's blur, so the pick
      // registers before close() would otherwise hide the panel first.
      btn.addEventListener('mousedown', (e) => {
        e.preventDefault();
        pick(currentOptions[Number(btn.dataset.index)]);
      });
    });
    panel.classList.remove('hidden');
  }

  function close() {
    panel.classList.add('hidden');
  }

  function pick(value) {
    if (multi) {
      const sel = selected();
      if (!sel.includes(value)) {
        hiddenEl.value = [...sel, value].join(',');
        renderChips();
      }
      input.value = '';
    } else {
      input.value = value;
      if (onSelect) onSelect(value);
    }
    close();
  }

  input.addEventListener('input', open);
  input.addEventListener('focus', open);
  input.addEventListener('blur', close);
  input.addEventListener('keydown', (e) => {
    if (panel.classList.contains('hidden')) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      highlighted = Math.min(highlighted + 1, currentOptions.length - 1);
      highlight();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      highlighted = Math.max(highlighted - 1, 0);
      highlight();
    } else if (e.key === 'Enter') {
      if (highlighted >= 0) {
        e.preventDefault();
        pick(currentOptions[highlighted]);
      }
    } else if (e.key === 'Escape') {
      close();
    }
  });

  if (multi) renderChips();

  return { refreshChips: renderChips, close };
}
