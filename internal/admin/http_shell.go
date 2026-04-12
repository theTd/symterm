package admin

const adminShellHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>symterm admin</title>
  <style>
    :root { --bg: #0e141b; --panel: #17222d; --accent: #72f1b8; --text: #e8eef5; --muted: #8ea0b3; --warn: #ffcf66; }
    body { margin: 0; font: 14px/1.5 "Iosevka", "Consolas", monospace; background: radial-gradient(circle at top, #1d2e3d, #0e141b 60%); color: var(--text); }
    header { padding: 18px 22px; border-bottom: 1px solid #273543; display: flex; justify-content: space-between; align-items: center; }
    main { display: grid; gap: 16px; padding: 18px; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); }
    section { background: rgba(23,34,45,.92); border: 1px solid #273543; border-radius: 14px; padding: 14px; box-shadow: 0 18px 60px rgba(0,0,0,.25); }
    h1,h2 { margin: 0 0 10px; }
    button { background: var(--accent); color: #0b1117; border: 0; padding: 8px 12px; border-radius: 999px; cursor: pointer; font: inherit; }
    button.warn { background: var(--warn); }
    pre { white-space: pre-wrap; word-break: break-word; color: var(--muted); }
    .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    input { background: #0d151d; color: var(--text); border: 1px solid #2f4355; border-radius: 10px; padding: 8px 10px; font: inherit; width: 100%; }
  </style>
</head>
<body>
  <header>
    <div>
      <h1>symterm admin</h1>
      <div id="status">not logged in</div>
    </div>
    <div class="row">
      <button id="login">login</button>
      <button id="logout" class="warn">logout</button>
    </div>
  </header>
  <main>
    <section><h2>Daemon</h2><pre id="daemon"></pre></section>
    <section>
      <h2>Sessions</h2>
      <div class="row"><input id="sessionId" placeholder="session id"><button id="terminateSession" class="warn">terminate</button></div>
      <pre id="sessions"></pre>
    </section>
    <section>
      <h2>Users</h2>
      <div class="row"><input id="newUser" placeholder="new username"><button id="createUser">create</button></div>
      <div class="row"><input id="disableUserName" placeholder="disable username"><button id="disableUser" class="warn">disable</button></div>
      <div class="row"><input id="issueTokenUser" placeholder="issue token for username"><button id="issueToken">issue token</button></div>
      <div class="row"><input id="entryUser" placeholder="entrypoint username"><input id="entryArgv" placeholder='argv json, e.g. ["bash","-lc"]'><button id="setEntrypoint">set entrypoint</button></div>
      <pre id="issuedToken"></pre>
      <pre id="users"></pre>
    </section>
  </main>
  <script>
    let state = null;
    let ws = null;
    let cursor = 0;
    function csrf() {
      const match = document.cookie.match(/symterm_admin_csrf=([^;]+)/);
      return match ? decodeURIComponent(match[1]) : "";
    }
    async function api(path, options = {}) {
      options.headers = Object.assign({}, options.headers || {}, { "X-CSRF-Token": csrf() });
      const res = await fetch(path, options);
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    }
    async function login() {
      await fetch('/admin/api/login', { method: 'POST' });
      document.getElementById('status').textContent = 'logged in';
      await reload();
      connectWS();
    }
    async function logout() {
      await api('/admin/api/logout', { method: 'POST' });
      document.getElementById('status').textContent = 'not logged in';
      if (ws) ws.close();
    }
    async function reload() {
      const snapshot = await api('/admin/api/snapshot');
      render(snapshot);
    }
    function render(snapshot) {
      state = snapshot;
      cursor = snapshot.cursor || cursor;
      document.getElementById('daemon').textContent = JSON.stringify(snapshot.daemon, null, 2);
      document.getElementById('sessions').textContent = JSON.stringify(snapshot.sessions, null, 2);
      document.getElementById('users').textContent = JSON.stringify(snapshot.users, null, 2);
    }
    function upsertBy(items, key, next) {
      const index = items.findIndex((item) => item[key] === next[key]);
      if (index >= 0) {
        items[index] = next;
        return items;
      }
      items.push(next);
      return items;
    }
    function applyEvent(event) {
      if (!state || !event) return;
      cursor = event.cursor || cursor;
      switch (event.kind) {
        case 'daemon_updated':
          if (event.daemon) state.daemon = event.daemon;
          break;
        case 'session_upsert':
          if (event.session) state.sessions = upsertBy(state.sessions || [], 'session_id', event.session);
          break;
        case 'session_closed':
          state.sessions = (state.sessions || []).filter((item) => item.session_id !== event.session_id);
          break;
        case 'user_upsert':
          if (event.user) state.users = upsertBy(state.users || [], 'username', event.user);
          break;
        case 'token_issued':
        case 'token_revoked':
          break;
      }
      state.cursor = cursor;
      render(state);
    }
    function connectWS() {
      if (ws) ws.close();
      ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/admin/ws?cursor=' + cursor);
      ws.onmessage = async (event) => {
        const payload = JSON.parse(event.data);
        if (payload.type === 'cursor_expired') {
          await reload();
          connectWS();
          return;
        }
        if (payload.type === 'event') {
          applyEvent(payload.event);
        }
      };
      ws.onclose = () => setTimeout(connectWS, 1000);
    }
    document.getElementById('login').onclick = login;
    document.getElementById('logout').onclick = logout;
    document.getElementById('createUser').onclick = async () => {
      const username = document.getElementById('newUser').value.trim();
      if (!username) return;
      await api('/admin/api/users', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username }) });
      await reload();
    };
    document.getElementById('disableUser').onclick = async () => {
      const username = document.getElementById('disableUserName').value.trim();
      if (!username) return;
      await api('/admin/api/users/' + encodeURIComponent(username) + '/disable', { method: 'POST' });
      await reload();
    };
    document.getElementById('issueToken').onclick = async () => {
      const username = document.getElementById('issueTokenUser').value.trim();
      if (!username) return;
      const token = await api('/admin/api/users/' + encodeURIComponent(username) + '/token', { method: 'POST' });
      document.getElementById('issuedToken').textContent = JSON.stringify(token, null, 2);
      await reload();
    };
    document.getElementById('setEntrypoint').onclick = async () => {
      const username = document.getElementById('entryUser').value.trim();
      const argvRaw = document.getElementById('entryArgv').value.trim();
      if (!username || !argvRaw) return;
      await api('/admin/api/users/' + encodeURIComponent(username) + '/entrypoint', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ entrypoint: JSON.parse(argvRaw) })
      });
      await reload();
    };
    document.getElementById('terminateSession').onclick = async () => {
      const sessionID = document.getElementById('sessionId').value.trim();
      if (!sessionID) return;
      await api('/admin/api/sessions/' + encodeURIComponent(sessionID) + '/terminate', { method: 'POST' });
      await reload();
    };
  </script>
</body>
</html>`
