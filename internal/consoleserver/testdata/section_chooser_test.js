const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

let focusedNode;

class TestNode {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.parent = null;
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this.hidden = false;
    this.required = false;
    this.disabled = false;
    this._selected = false;
    this._value = '';
    this._text = '';
    this.validationMessage = '';
    this.classes = new Set();
    this.classList = {
      add: (...names) => names.forEach(name => this.classes.add(name)),
      remove: (...names) => names.forEach(name => this.classes.delete(name)),
      contains: name => this.classes.has(name),
    };
  }

  append(...nodes) {
    for (const node of nodes) {
      node.parent = this;
      this.children.push(node);
    }
  }

  replaceChildren(...nodes) {
    for (const child of this.children) child.parent = null;
    this.children = [];
    this.append(...nodes);
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  async dispatch(type, event = {}) {
    for (const listener of this.listeners.get(type) || []) await listener(event);
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  setCustomValidity(message) {
    this.validationMessage = message;
  }

  focus() {
    focusedNode = this;
  }

  set textContent(value) {
    this._text = String(value);
    this.children = [];
  }

  get textContent() {
    return this.children.length
      ? this.children.map(child => child.textContent).join('')
      : this._text;
  }

  get options() {
    const options = [];
    const collect = node => {
      for (const child of node.children) {
        if (child.tag === 'option') options.push(child);
        else collect(child);
      }
    };
    collect(this);
    return options;
  }

  set selected(selected) {
    if (selected) {
      const select = this.closestSelect();
      if (select) select.options.forEach(option => { option._selected = false; });
    }
    this._selected = selected;
  }

  get selected() {
    return this._selected;
  }

  closestSelect() {
    let node = this.parent;
    while (node && node.tag !== 'select') node = node.parent;
    return node;
  }

  set selectedIndex(index) {
    this.options.forEach((option, optionIndex) => {
      option._selected = optionIndex === index;
    });
  }

  get selectedIndex() {
    return this.options.findIndex(option => option.selected);
  }

  get selectedOptions() {
    return this.options.filter(option => option.selected);
  }

  set value(value) {
    const match = this.options.find(option => option.value === String(value));
    this.options.forEach(option => { option._selected = option === match; });
    this._value = String(value);
  }

  get value() {
    if (this.tag === 'select') return this.selectedOptions[0]?.value || '';
    return this._value;
  }
}

global.document = {
  createElement: tag => new TestNode(tag),
};
const storage = new Map();
global.window = {
  localStorage: {
    getItem: key => storage.has(key) ? storage.get(key) : null,
    setItem: (key, value) => storage.set(key, String(value)),
  },
  setTimeout: callback => callback(),
};

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('function addOption', 'function credentialTypeLabel'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function renderSessions', 'function sectionLabel'), {filename: 'app.js'});
global.sectionOrderStoragePrefix = 'kelos-session-section-order:';
vm.runInThisContext(applicationSlice('function sectionLabel', 'function sessionSectionNames'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function sessionSectionNames', 'async function loadSessions'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function updateSelectedSectionField', "elements.sectionChoice.addEventListener"), {filename: 'app.js'});

function resetHarness() {
  focusedNode = null;
  global.elements = {
    sectionSelect: new TestNode('select'),
    sectionCustom: new TestNode('input'),
    sectionChoice: new TestNode('select'),
    sectionChoiceCustom: new TestNode('input'),
    sectionForm: new TestNode('form'),
    saveSectionButton: new TestNode('button'),
    cancelSectionButton: new TestNode('button'),
  };
  elements.sectionForm.reportValidity = () => true;
  global.state = {
    sessions: [],
    selected: null,
    sectionSaving: false,
    sectionAssignments: new Set(),
    sectionOrders: new Map(),
    sidebarDrag: null,
    namespace: 'default',
    namespaceGeneration: 0,
  };
  global.sessionKey = session => `${session.namespace}/${session.name}`;
  storage.clear();
}

function group(select, label) {
  return select.children.find(child => child.tag === 'optgroup' && child.label === label);
}

function option(groupNode, label) {
  return groupNode.children.find(child => child.textContent === label);
}

function testSectionOptionsDistinguishActionsFromValidNames() {
  resetHarness();
  state.sessions = [
    {section: 'Unsectioned (remove assignment)'},
    {section: '＋ Create new section…'},
    {section: 'Planning'},
    {section: 'Planning'},
  ];

  populateSectionSelect(
    elements.sectionChoice,
    'Unsectioned (remove assignment)',
    'Unsectioned (remove assignment)',
  );

  const actions = group(elements.sectionChoice, 'Actions');
  const existing = group(elements.sectionChoice, 'Existing sections');
  assert.ok(actions);
  assert.ok(existing);
  assert.ok(option(actions, 'Unsectioned (remove assignment)'));
  assert.ok(option(existing, 'Unsectioned (remove assignment)'));
  assert.ok(option(actions, '＋ Create new section…'));
  assert.ok(option(existing, '＋ Create new section…'));
  assert.equal(elements.sectionChoice.value, 'Unsectioned (remove assignment)');
  assert.equal(elements.sectionChoice.selectedOptions[0].parent, existing);
}

function testSectionPayloadsAndValidation() {
  resetHarness();
  state.sessions = [{section: 'Planning'}];
  populateSectionSelect(elements.sectionChoice, '', 'Unsectioned (remove assignment)');

  elements.sectionChoice.value = 'Planning';
  assert.deepEqual(selectedSectionPayload(elements.sectionChoice, elements.sectionChoiceCustom), {section: 'Planning'});

  elements.sectionChoice.selectedIndex = 0;
  assert.deepEqual(selectedSectionPayload(elements.sectionChoice, elements.sectionChoiceCustom), {});
  assert.deepEqual(selectedSectionPayload(elements.sectionChoice, elements.sectionChoiceCustom, true), {section: ''});

  option(group(elements.sectionChoice, 'Actions'), '＋ Create new section…').selected = true;
  updateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  elements.sectionChoiceCustom.value = '   ';
  validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  assert.equal(elements.sectionChoiceCustom.validationMessage, 'Enter a section name');

  elements.sectionChoiceCustom.value = '  Reviews  ';
  validateCustomSectionField(elements.sectionChoice, elements.sectionChoiceCustom);
  assert.equal(elements.sectionChoiceCustom.validationMessage, '');
  assert.deepEqual(selectedSectionPayload(elements.sectionChoice, elements.sectionChoiceCustom), {section: 'Reviews'});
}

function testRefreshPreservesCustomInput() {
  resetHarness();
  state.sessions = [{section: 'Planning'}];
  renderSectionOptions();
  option(group(elements.sectionSelect, 'Actions'), '＋ Create new section…').selected = true;
  updateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
  elements.sectionCustom.value = 'Backlog';

  state.sessions.push({section: 'Reviews'});
  renderSectionOptions();

  assert.equal(createsNewSection(elements.sectionSelect), true);
  assert.equal(elements.sectionCustom.hidden, false);
  assert.equal(elements.sectionCustom.value, 'Backlog');
  assert.ok(option(group(elements.sectionSelect, 'Existing sections'), 'Reviews'));
}

function testNamespaceResetClearsSectionInput() {
  resetHarness();
  state.sessions = [{section: 'Planning'}];
  renderSectionOptions();
  option(group(elements.sectionSelect, 'Actions'), '＋ Create new section…').selected = true;
  updateCustomSectionField(elements.sectionSelect, elements.sectionCustom);
  elements.sectionCustom.value = 'Stale section';

  resetSectionSelection();
  state.sessions = [];
  renderSectionOptions();

  assert.equal(createsNewSection(elements.sectionSelect), false);
  assert.equal(elements.sectionSelect.selectedIndex, 0);
  assert.equal(elements.sectionCustom.hidden, true);
  assert.equal(elements.sectionCustom.value, '');
}

function testSelectedSessionUsesInlineChooser() {
  resetHarness();
  const planning = {namespace: 'default', name: 'demo', section: 'Planning'};
  const reviews = {namespace: 'default', name: 'other', section: 'Reviews'};
  state.sessions = [planning, reviews];
  state.selected = planning;

  renderSelectedSessionSection(planning);

  assert.equal(elements.sectionForm.hidden, false);
  assert.equal(elements.sectionChoice.value, 'Planning');
  assert.equal(elements.sectionChoice.disabled, false);
  assert.equal(elements.sectionChoiceCustom.hidden, true);
  assert.equal(elements.saveSectionButton.hidden, true);

  option(group(elements.sectionChoice, 'Actions'), '＋ Create new section…').selected = true;
  updateSelectedSectionField();
  elements.sectionChoiceCustom.value = 'Backlog';
  renderSelectedSessionSection(planning);

  assert.equal(elements.sectionChoiceCustom.hidden, false);
  assert.equal(elements.sectionChoiceCustom.required, true);
  assert.equal(elements.sectionChoiceCustom.value, 'Backlog');
  assert.equal(elements.saveSectionButton.hidden, false);

  state.selected = reviews;
  renderSelectedSessionSection(reviews);
  assert.equal(elements.sectionChoice.value, 'Reviews');
  assert.equal(elements.sectionChoiceCustom.hidden, true);

  state.selected = null;
  renderSelectedSessionSection(null);
  assert.equal(elements.sectionForm.hidden, true);
  assert.equal(elements.sectionChoice.disabled, true);
}

function testSectionOrderingIncludesUnsectioned() {
  resetHarness();
  state.sessions = [
    {section: 'Reviews'},
    {section: 'Planning'},
  ];

  assert.deepEqual(orderedSessionSections(), ['Planning', 'Reviews', '']);
  assert.equal(storeSectionOrder(['', 'Reviews', 'Planning']), true);
  assert.deepEqual(orderedSessionSections(), ['', 'Reviews', 'Planning']);
  assert.deepEqual(
    JSON.parse(storage.get('kelos-session-section-order:default')),
    ['', 'Reviews', 'Planning'],
  );

  state.sectionOrders.clear();
  assert.deepEqual(orderedSessionSections(), ['', 'Reviews', 'Planning']);
}

function testUnsectionedCanMoveLikeNamedSections() {
  assert.deepEqual(
    reorderSections(['Planning', '', 'Reviews'], '', 'Planning', false),
    ['', 'Planning', 'Reviews'],
  );
  assert.deepEqual(
    reorderSections(['Planning', '', 'Reviews'], '', 'Reviews', true),
    ['Planning', 'Reviews', ''],
  );
}

function testSectionOrderFocusFollowsMovedSection() {
  resetHarness();
  const up = new TestNode('button');
  up.dataset.section = 'Planning';
  up.dataset.direction = '-1';
  const down = new TestNode('button');
  down.dataset.section = 'Planning';
  down.dataset.direction = '1';
  const other = new TestNode('button');
  other.dataset.section = 'Reviews';
  other.dataset.direction = '-1';
  elements.list = {querySelectorAll: () => [up, down, other]};
  let renderCount = 0;
  global.renderSessions = () => { renderCount++; };
  global.showToast = () => {};

  applySectionOrder(['Planning', 'Reviews', ''], 'Planning', -1);
  assert.equal(renderCount, 1);
  assert.equal(focusedNode, up);

  up.disabled = true;
  focusSectionOrderControl('Planning', -1);
  assert.equal(focusedNode, down);
}

async function testDragAssignmentMovesSession() {
  resetHarness();
  const session = {namespace: 'default', name: 'demo', section: 'Planning'};
  state.sessions = [session, {namespace: 'default', name: 'other', section: 'Reviews'}];
  state.selected = session;
  let request;
  let renderCount = 0;
  const toasts = [];
  global.api = async (path, options) => {
    request = {path, options};
    return {...session, section: 'Reviews'};
  };
  global.sessionKey = value => `${value.namespace}/${value.name}`;
  global.renderSessions = () => { renderCount++; };
  global.renderHeader = () => {};
  global.showToast = message => { toasts.push(message); };

  await moveSessionToSection(session, 'Reviews');

  assert.equal(request.path, '/api/sessions/default/demo/section');
  assert.deepEqual(JSON.parse(request.options.body), {section: 'Reviews'});
  assert.equal(state.sessions[0].section, 'Reviews');
  assert.equal(state.selected.section, 'Reviews');
  assert.equal(state.sectionAssignments.size, 0);
  assert.ok(renderCount >= 2);
  assert.deepEqual(toasts, ['Moved Session to Reviews']);
}

async function testPendingInlineSaveDisablesChooser() {
  resetHarness();
  state.sessions = [
    {namespace: 'default', name: 'demo', section: 'Planning'},
    {namespace: 'default', name: 'other', section: 'Reviews'},
  ];
  state.selected = state.sessions[0];
  renderSelectedSessionSection(state.selected);
  elements.sectionChoice.value = 'Reviews';

  let resolveRequest;
  let request;
  let requestCount = 0;
  global.api = (path, options) => {
    requestCount++;
    request = {path, options};
    return new Promise(resolve => { resolveRequest = resolve; });
  };
  global.renderSessions = () => {};
  global.renderHeader = () => renderSelectedSessionSection(state.selected);
  global.showToast = () => {};

  const submission = saveSelectedSessionSection('Reviews');
  assert.equal(state.sectionSaving, true);
  assert.equal(elements.sectionChoice.disabled, true);
  assert.equal(elements.sectionChoiceCustom.disabled, true);
  assert.equal(elements.saveSectionButton.disabled, true);
  assert.equal(elements.sectionForm.attributes.get('aria-busy'), 'true');

  await saveSelectedSessionSection('Reviews');
  assert.equal(requestCount, 1);

  assert.equal(request.path, '/api/sessions/default/demo/section');
  assert.deepEqual(JSON.parse(request.options.body), {section: 'Reviews'});
  resolveRequest({namespace: 'default', name: 'demo', section: 'Reviews'});
  await submission;

  assert.equal(state.sectionSaving, false);
  assert.equal(elements.sectionChoice.disabled, false);
  assert.equal(elements.sectionChoice.value, 'Reviews');
  assert.equal(elements.sectionForm.attributes.get('aria-busy'), 'false');
}

async function testInlineNewSectionSubmission() {
  resetHarness();
  const session = {namespace: 'default', name: 'demo', section: 'Planning'};
  state.sessions = [session];
  state.selected = session;
  let request;
  global.api = async (path, options) => {
    request = {path, options};
    return {...session, section: JSON.parse(options.body).section};
  };
  global.renderSessions = () => {};
  global.renderHeader = () => renderSelectedSessionSection(state.selected);
  global.showToast = () => {};

  vm.runInThisContext(
    applicationSlice("elements.sectionChoice.addEventListener", "elements.resumeButton.addEventListener"),
    {filename: 'app.js'},
  );
  renderSelectedSessionSection(session);
  option(group(elements.sectionChoice, 'Actions'), '＋ Create new section…').selected = true;
  await elements.sectionChoice.dispatch('change');

  assert.equal(elements.sectionChoiceCustom.hidden, false);
  assert.equal(elements.saveSectionButton.hidden, false);
  assert.equal(focusedNode, elements.sectionChoiceCustom);

  elements.sectionChoiceCustom.value = '  Backlog  ';
  await elements.sectionChoiceCustom.dispatch('input');
  await elements.sectionForm.dispatch('submit', {preventDefault() {}});

  assert.equal(request.path, '/api/sessions/default/demo/section');
  assert.deepEqual(JSON.parse(request.options.body), {section: 'Backlog'});
  assert.equal(state.selected.section, 'Backlog');
  assert.equal(elements.sectionChoice.value, 'Backlog');
  assert.equal(elements.sectionChoiceCustom.hidden, true);
}

testSectionOptionsDistinguishActionsFromValidNames();
testSectionPayloadsAndValidation();
testRefreshPreservesCustomInput();
testNamespaceResetClearsSectionInput();
testSelectedSessionUsesInlineChooser();
testSectionOrderingIncludesUnsectioned();
testUnsectionedCanMoveLikeNamedSections();
testSectionOrderFocusFollowsMovedSection();
testDragAssignmentMovesSession()
  .then(testPendingInlineSaveDisablesChooser)
  .then(testInlineNewSectionSubmission)
  .then(() => {
    process.stdout.write('Section chooser tests passed\n');
  });
