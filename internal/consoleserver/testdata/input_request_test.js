const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class TestNode {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.parent = null;
    this.classes = new Set();
    this.listeners = new Map();
    this.checked = false;
    this.disabled = false;
    this.value = '';
    this._text = '';
  }

  append(...nodes) {
    for (const node of nodes) {
      node.parent = this;
      this.children.push(node);
    }
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  dispatch(type, event = {}) {
    for (const listener of this.listeners.get(type) || []) listener(event);
  }

  focus() {
    this.focused = true;
  }

  set className(value) {
    this.classes = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get className() {
    return [...this.classes].join(' ');
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
}

global.document = {
  createElement: tag => new TestNode(tag),
};
global.WebSocket = {OPEN: 1};
global.ensureConversation = () => {};
global.scrollToBottom = () => {};

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
const start = application.indexOf('function bindOtherAnswer');
const end = application.indexOf('function resolveInputCard', start);
assert.notEqual(start, -1, 'bindOtherAnswer not found');
assert.notEqual(end, -1, 'resolveInputCard not found');
vm.runInThisContext(application.slice(start, end), {filename: 'app.js'});

let sentMessages;
let toasts;

function resetHarness() {
  sentMessages = [];
  toasts = [];
  global.elements = {messages: new TestNode('div')};
  global.state = {
    inputs: new Map(),
    socket: {
      readyState: WebSocket.OPEN,
      send: message => sentMessages.push(JSON.parse(message)),
    },
  };
  global.showToast = message => toasts.push(message);
}

function findAll(node, predicate) {
  const matches = predicate(node) ? [node] : [];
  for (const child of node.children) matches.push(...findAll(child, predicate));
  return matches;
}

function formControls(form) {
  return {
    options: findAll(form, node => node.tag === 'input' && !node.classes.has('input-other')),
    other: findAll(form, node => node.classes.has('input-other'))[0],
  };
}

function submit(form) {
  form.dispatch('submit', {preventDefault: () => {}});
}

function testSingleChoiceFormKeepsAnswerExclusive() {
  resetHarness();
  renderInputRequest({
    inputId: 'single-choice',
    questions: [{
      id: 'answer',
      header: 'Answer',
      question: 'Choose one',
      options: [{label: 'First'}, {label: 'Second'}],
    }],
  });

  const form = elements.messages.children[0];
  const {options, other} = formControls(form);
  options[0].checked = true;
  other.value = '   ';
  other.dispatch('input');
  assert.equal(options[0].checked, true);
  submit(form);
  assert.deepEqual(sentMessages.pop(), {
    type: 'input',
    inputId: 'single-choice',
    answers: {answer: ['First']},
  });

  other.value = 'Another answer';
  other.dispatch('input');
  assert.equal(options[0].checked, false);
  submit(form);
  assert.deepEqual(sentMessages.pop(), {
    type: 'input',
    inputId: 'single-choice',
    answers: {answer: ['Another answer']},
  });

  options[1].checked = true;
  options[1].dispatch('change');
  assert.equal(other.value, '');
  submit(form);
  assert.deepEqual(sentMessages.pop(), {
    type: 'input',
    inputId: 'single-choice',
    answers: {answer: ['Second']},
  });
  assert.deepEqual(toasts, []);
}

function testMultipleChoiceFormAllowsOptionsAndOtherText() {
  resetHarness();
  renderInputRequest({
    inputId: 'multiple-choice',
    questions: [{
      id: 'answers',
      header: 'Answers',
      question: 'Choose several',
      multiSelect: true,
      options: [{label: 'First'}, {label: 'Second'}],
    }],
  });

  const form = elements.messages.children[0];
  const {options, other} = formControls(form);
  options[0].checked = true;
  other.value = 'Another answer';
  other.dispatch('input');
  assert.equal(options[0].checked, true);
  submit(form);
  assert.deepEqual(sentMessages, [{
    type: 'input',
    inputId: 'multiple-choice',
    answers: {answers: ['First', 'Another answer']},
  }]);
  assert.deepEqual(toasts, []);
}

testSingleChoiceFormKeepsAnswerExclusive();
testMultipleChoiceFormAllowsOptionsAndOtherText();
