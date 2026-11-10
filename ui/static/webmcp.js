// WebMCP tools for the dashboard. Where the browser offers WebMCP
// (navigator.modelContext or document.modelContext), this script registers
// one tool per factory action, from the list the server generates from the
// action registry. A tool calls the REST surface on this site with the
// person's session cookie and the session's CSRF token, so it has exactly
// the person's authority and no more: the server checks roles and scopes on
// every call, and a tool description grants nothing. Where WebMCP is
// missing, the page shows the fallback notice instead.
//
// Every request stays on this site, and the CSRF token is never logged or
// stored beyond the call that uses it.
(function () {
  'use strict';

  var LIST_PATH = '/webmcp/actions.json';
  var SESSION_PATH = '/auth/session';
  var CSRF_HEADER = 'X-CSRF-Token';
  var FALLBACK_NOTICE = 'This browser does not offer WebMCP. Use the dashboard, the REST API, ' +
    'the sisf CLI or the remote MCP endpoint instead; they share the same actions and permissions.';
  var UNAVAILABLE_NOTICE = 'Factory tools could not be offered through WebMCP. Sign in, then reload the page.';

  // modelContext returns the WebMCP entry point, or null where the browser
  // does not offer one.
  function modelContext() {
    if (typeof navigator !== 'undefined' && navigator.modelContext) {
      return navigator.modelContext;
    }
    if (typeof document !== 'undefined' && document.modelContext) {
      return document.modelContext;
    }
    return null;
  }

  // showStatus writes a notice into every element marked data-webmcp-status.
  function showStatus(text) {
    var nodes = document.querySelectorAll('[data-webmcp-status]');
    for (var i = 0; i < nodes.length; i++) {
      nodes[i].textContent = text;
      nodes[i].hidden = false;
    }
  }

  // isLocalPath keeps every request on this site: an absolute path that is
  // not protocol-relative.
  function isLocalPath(path) {
    return typeof path === 'string' && path.charAt(0) === '/' && path.charAt(1) !== '/' &&
      path.indexOf('\\') === -1;
  }

  var request = typeof window.fetch === 'function' ? window.fetch.bind(window) : null;

  function send(path, init) {
    if (!isLocalPath(path)) {
      return Promise.reject(new Error('refusing a request that leaves this site'));
    }
    init.credentials = 'same-origin';
    init.redirect = 'error';
    return request(path, init);
  }

  function getJSON(path) {
    return send(path, { method: 'GET', headers: { 'Accept': 'application/json' } }).then(function (resp) {
      if (!resp.ok) {
        throw new Error('GET ' + path + ' answered ' + resp.status);
      }
      return resp.json();
    });
  }

  // csrfToken reads the current session's CSRF token for one call.
  function csrfToken() {
    return getJSON(SESSION_PATH).then(function (session) {
      if (!session || typeof session.csrf_token !== 'string' || session.csrf_token === '') {
        throw new Error('no signed-in session');
      }
      return session.csrf_token;
    });
  }

  // callAction posts the tool's arguments, the REST envelope of input,
  // dry_run, idempotency_key and approval_id, to the action's REST path,
  // and hands the typed answer back to the agent unchanged.
  function callAction(tool, args) {
    return csrfToken().then(function (token) {
      var headers = { 'Content-Type': 'application/json', 'Accept': 'application/json' };
      headers[CSRF_HEADER] = token;
      return send(tool.path, { method: 'POST', headers: headers, body: JSON.stringify(args || {}) });
    }).then(function (resp) {
      return resp.text().then(function (text) {
        return { content: [{ type: 'text', text: text }], isError: !resp.ok };
      });
    }, function (err) {
      return { content: [{ type: 'text', text: JSON.stringify({ error: { code: 'UNAUTHENTICATED', message: String(err.message) } }) }], isError: true };
    });
  }

  function toTool(entry) {
    return {
      name: entry.name,
      description: entry.description,
      inputSchema: entry.inputSchema,
      annotations: { readOnlyHint: entry.readOnly === true },
      execute: function (args) { return callAction(entry, args); }
    };
  }

  function register(context, tools) {
    if (typeof context.registerTool === 'function') {
      tools.forEach(function (tool) { context.registerTool(tool); });
      return;
    }
    if (typeof context.provideContext === 'function') {
      context.provideContext({ tools: tools });
      return;
    }
    throw new Error('this WebMCP version offers no way to register tools');
  }

  function start() {
    var context = modelContext();
    if (!context || !request) {
      showStatus(FALLBACK_NOTICE);
      return;
    }
    csrfToken().then(function () {
      return getJSON(LIST_PATH);
    }).then(function (list) {
      var entries = (list && list.tools) || [];
      var tools = entries.filter(function (entry) { return isLocalPath(entry.path); }).map(toTool);
      register(context, tools);
      showStatus('WebMCP: ' + tools.length + ' factory tools are offered to this browser\'s agent.');
    }).catch(function () {
      showStatus(UNAVAILABLE_NOTICE);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
