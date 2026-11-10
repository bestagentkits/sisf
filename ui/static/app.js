// Progressive enhancement for the read-only dashboard. Every page works
// without it; the script reads nothing from the network and holds no secret.
(function () {
  'use strict';

  // Narrow the visible workboard rows to those containing the typed text.
  var filter = document.getElementById('row-filter');
  if (filter) {
    filter.addEventListener('input', function () {
      var needle = filter.value.toLowerCase();
      document.querySelectorAll('tr.issue').forEach(function (row) {
        row.hidden = needle !== '' && row.textContent.toLowerCase().indexOf(needle) === -1;
      });
    });
  }

  // Ask before a form that carries a data-confirm message is sent. The
  // server still decides what the request may do; this only prevents a slip.
  document.querySelectorAll('form[data-confirm]').forEach(function (form) {
    form.addEventListener('submit', function (event) {
      if (!window.confirm(form.getAttribute('data-confirm'))) {
        event.preventDefault();
      }
    });
  });

  // Reload the page on a timer while the box is ticked.
  var auto = document.getElementById('auto-refresh');
  if (auto) {
    var timer = null;
    auto.addEventListener('change', function () {
      if (timer !== null) {
        clearInterval(timer);
        timer = null;
      }
      if (auto.checked) {
        timer = setInterval(function () { location.reload(); }, 15000);
      }
    });
  }
})();
