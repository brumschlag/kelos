const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function sessionKey', 'function sessionViewKey'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function sessionRuntimePath', 'function renderRuntimeStatus'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function saveSessionDisplayName', 'async function moveSessionToSection'), {filename: 'app.js'});

async function testDisplayNameUpdatePreservesSessionIdentity() {
  const session = {namespace: 'team-a', name: 'chat', displayName: 'chat'};
  global.state = {
    namespaceGeneration: 3,
    sessions: [session],
    selected: session,
  };
  let request;
  let sessionsRendered = 0;
  let headerRendered = 0;
  let runtimeStatusRendered = 0;
  global.api = async (requestPath, options) => {
    request = {path: requestPath, options};
    return {...session, displayName: 'Investigate flaky CI'};
  };
  global.renderSessions = () => { sessionsRendered++; };
  global.renderHeader = () => {
    headerRendered++;
    renderRuntimeStatus();
  };
  global.renderRuntimeStatus = () => { runtimeStatusRendered++; };

  await saveSessionDisplayName(session, 'Investigate flaky CI');

  assert.equal(request.path, '/api/sessions/team-a/chat/display-name');
  assert.deepEqual(JSON.parse(request.options.body), {displayName: 'Investigate flaky CI'});
  assert.equal(state.sessions[0].name, 'chat');
  assert.equal(state.sessions[0].displayName, 'Investigate flaky CI');
  assert.equal(state.selected.displayName, 'Investigate flaky CI');
  assert.equal(sessionsRendered, 1);
  assert.equal(headerRendered, 1);
  assert.equal(runtimeStatusRendered, 1);
}

function testDisplayNameRenderingFallsBackToResourceName() {
  assert.equal(sessionDisplayName({name: 'chat', displayName: 'Investigate flaky CI'}), 'Investigate flaky CI');
  assert.equal(sessionDisplayName({name: 'chat'}), 'chat');
  assert.equal(
    sessionRuntimeStatusText({sessionName: 'chat', agentType: 'codex'}, 'Investigate flaky CI'),
    'Investigate flaky CI · codex',
  );
}

testDisplayNameRenderingFallsBackToResourceName();
testDisplayNameUpdatePreservesSessionIdentity().then(() => {
  process.stdout.write('Display name tests passed\n');
});
