(() => {
  if (window.__gmclRulesAssistantLoaded) return;
  window.__gmclRulesAssistantLoaded = true;

  const config = window.GMCLRulesAssistantConfig || {};
  const chatEndpoint = config.chatEndpoint || '/api/rules/chat';
  const feedbackEndpoint = config.feedbackEndpoint || '/api/rules/chat/feedback';
  const requestHeaders = {'Content-Type': 'application/json'};
  if (config.csrfToken) requestHeaders['X-CSRF-Token'] = config.csrfToken;

  const createElement = (tag, className, text) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
  };

  const setupScope = root => {
    if (!root) return () => ({});
    root.querySelectorAll('button[data-scope-key]').forEach(button => {
      button.addEventListener('click', () => {
        const wasPressed = button.getAttribute('aria-pressed') === 'true';
        root.querySelectorAll(`button[data-scope-key="${button.dataset.scopeKey}"]`).forEach(peer => peer.setAttribute('aria-pressed', 'false'));
        button.setAttribute('aria-pressed', String(!wasPressed));
      });
    });
    return () => {
      const selected = {};
      root.querySelectorAll('button[aria-pressed="true"]').forEach(button => { selected[button.dataset.scopeKey] = button.dataset.scopeValue; });
      return selected;
    };
  };

  const addMessage = (messages, kind, text) => {
    const element = createElement('div', `rules-message ${kind}`, text);
    messages.appendChild(element);
    messages.scrollTop = messages.scrollHeight;
    return element;
  };

  const addFeedback = (container, messageId) => {
    if (!messageId) return;
    const row = createElement('div', 'rules-feedback');
    [['helpful', 'Helpful'], ['unhelpful', 'Not helpful'], ['report', 'Report']].forEach(([rating, label]) => {
      const button = createElement('button', '', label);
      button.type = 'button';
      button.addEventListener('click', async () => {
        button.disabled = true;
        try {
          await fetch(feedbackEndpoint, {
            method: 'POST',
            headers: requestHeaders,
            body: JSON.stringify({message_id: messageId, rating})
          });
          row.textContent = 'Thank you for the feedback.';
        } catch (_) {
          button.disabled = false;
        }
      });
      row.appendChild(button);
    });
    container.appendChild(row);
  };

  const renderAnswer = (messages, data) => {
    const element = addMessage(messages, 'assistant', data.answer);
    if (data.clarification_questions?.length) {
      const followups = createElement('div', 'rules-clarifications');
      followups.appendChild(createElement('strong', '', 'A1 needs to clarify:'));
      const list = createElement('ul');
      data.clarification_questions.forEach(value => list.appendChild(createElement('li', '', value)));
      followups.appendChild(list);
      element.appendChild(followups);
    }
    if (data.applicable_conditions?.length) {
      const list = createElement('ul', 'rules-conditions');
      data.applicable_conditions.forEach(value => list.appendChild(createElement('li', '', value)));
      element.appendChild(list);
    }
    if (data.citations?.length) {
      const list = createElement('ol', 'rules-citations');
      data.citations.forEach(citation => {
        const item = createElement('li');
        const link = createElement('a', '', `${citation.rule_reference ? `Rule ${citation.rule_reference} — ` : ''}${citation.title}`);
        link.href = citation.url;
        link.target = '_blank';
        link.rel = 'noopener';
        item.appendChild(link);
        list.appendChild(item);
      });
      element.appendChild(list);
    }
    element.appendChild(createElement('div', 'rules-meta', `Rules snapshot: ${data.rules_as_of}`));
    addFeedback(element, data.message_id);
  };

  const readSSE = async (response, handlers) => {
    if (!response.body) throw new Error('Streaming is unavailable');
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    while (true) {
      const {value, done} = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, {stream: true}).replace(/\r\n/g, '\n');
      let boundary;
      while ((boundary = buffer.indexOf('\n\n')) >= 0) {
        const block = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        let event = '';
        let data = '';
        block.split('\n').forEach(line => {
          if (line.startsWith('event:')) event = line.slice(6).trim();
          if (line.startsWith('data:')) data += line.slice(5).trim();
        });
        if (data && handlers[event]) handlers[event](JSON.parse(data));
      }
    }
  };

  const connectChat = ({form, input, messages, status, button, getScope = () => ({})}) => {
    form.addEventListener('submit', async event => {
      event.preventDefault();
      const question = input.value.trim();
      if (question.length < 3 || button.disabled) return;
      addMessage(messages, 'user', question);
      input.value = '';
      button.disabled = true;
      status.textContent = 'Searching the published rules…';
      status.classList.add('is-thinking');
      try {
        const response = await fetch(chatEndpoint, {
          method: 'POST',
          headers: {...requestHeaders, 'Accept': 'text/event-stream'},
          body: JSON.stringify({question, ...getScope()})
        });
        if (!response.ok) throw new Error(await response.text());
        await readSSE(response, {
          status: data => { status.textContent = data.message; },
          answer: data => renderAnswer(messages, data),
          error: data => addMessage(messages, 'assistant', data.message)
        });
      } catch (_) {
        addMessage(messages, 'assistant', 'I cannot search the rules just now. Please try again shortly or open the full A1 Rules Assistant for more information.');
      } finally {
        button.disabled = false;
        status.textContent = '';
        status.classList.remove('is-thinking');
        input.focus();
      }
    });
  };

  const pageForm = document.getElementById('rules-form');
  if (pageForm) {
    const getPageScope = setupScope(document.querySelector('[data-rules-scope]'));
    connectChat({
      form: pageForm,
      input: document.getElementById('rules-question'),
      messages: document.getElementById('rules-messages'),
      status: document.getElementById('rules-status'),
      button: pageForm.querySelector('button[type="submit"]'),
      getScope: getPageScope
    });
  }

  if (location.pathname === '/rules-assistant' && !config.admin) return;

  const assistantScope = config.admin ? 'Published rules and approved sanctions' : 'Published rules only';
  const greeting = config.admin
    ? '<strong>Hello!</strong> Ask me about the published rules, or why a named club has cards or other approved sanctions.'
    : '<strong>Hello!</strong> Ask me a question about the published GMCL cricket rules. I’ll show the source beside my answer.';

  const widget = createElement('div', 'rules-widget');
  widget.innerHTML = `
    <section class="rules-widget-panel" id="rules-widget-panel" aria-label="A1 Rules Assistant" hidden>
      <header class="rules-widget-header">
        <img src="/images/gmcl-rules-bot-avatar.png" alt="" class="rules-widget-avatar" width="48" height="48">
        <div class="rules-widget-heading">
          <strong>A1 Rules Assistant</strong>
          <span><i aria-hidden="true"></i> ${assistantScope}</span>
        </div>
        <button type="button" class="rules-widget-close" aria-label="Close A1 Rules Assistant">×</button>
      </header>
      <div class="rules-widget-messages rules-messages" aria-live="polite">
        <div class="rules-message assistant">${greeting}</div>
        <div class="rules-widget-prompts" aria-label="Suggested questions">
          <button type="button">Who is eligible to play?</button>
          <button type="button">What happens in bad weather?</button>
          <button type="button">${config.admin ? 'Why does a club have cards?' : 'Find a rule number'}</button>
        </div>
      </div>
      <div class="rules-scope rules-widget-scope" data-rules-scope aria-label="Optional match context">
        <span class="rules-scope-label">Match context <small>(optional)</small></span>
        <div class="rules-scope-groups">
          <div class="rules-scope-group" aria-label="Player level"><button type="button" data-scope-key="level" data-scope-value="senior" aria-pressed="false">Senior</button><button type="button" data-scope-key="level" data-scope-value="junior" aria-pressed="false">Junior</button></div>
          <div class="rules-scope-group" aria-label="Competition type"><button type="button" data-scope-key="competition" data-scope-value="league" aria-pressed="false">League</button><button type="button" data-scope-key="competition" data-scope-value="cup" aria-pressed="false">Cup</button></div>
        </div>
      </div>
      <div class="rules-widget-status" role="status"></div>
      <form class="rules-widget-form">
        <label class="visually-hidden" for="rules-widget-question">Ask a rules question</label>
        <textarea id="rules-widget-question" maxlength="1200" rows="2" placeholder="${config.admin ? 'Ask about rules or name a club…' : 'Ask about a GMCL rule…'}" required></textarea>
        <button type="submit" aria-label="Send question">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 12h14M13 6l6 6-6 6"/></svg>
        </button>
      </form>
      <footer class="rules-widget-footer"><a href="${config.fullURL || '/rules-assistant'}">${config.admin ? 'Assistant controls and history' : 'Open full assistant'}</a><span>Informational, not an official ruling</span></footer>
    </section>
    <button type="button" class="rules-widget-launcher" aria-expanded="false" aria-controls="rules-widget-panel">
      <span class="rules-widget-launch-copy"><strong>Ask A1 about GMCL rules</strong><small>Open A1 Rules Assistant</small></span>
      <span class="rules-widget-bot-wrap"><span class="rules-widget-online" aria-hidden="true"></span><img src="/images/gmcl-rules-bot-avatar.png" alt="" width="68" height="68"></span>
      <span class="visually-hidden">Open A1 Rules Assistant</span>
    </button>`;
  document.body.appendChild(widget);

  const launcher = widget.querySelector('.rules-widget-launcher');
  const panel = widget.querySelector('.rules-widget-panel');
  const close = widget.querySelector('.rules-widget-close');
  const form = widget.querySelector('.rules-widget-form');
  const input = widget.querySelector('textarea');
  const messages = widget.querySelector('.rules-widget-messages');
  const status = widget.querySelector('.rules-widget-status');
  const send = form.querySelector('button[type="submit"]');
  const getWidgetScope = setupScope(widget.querySelector('[data-rules-scope]'));

  const setOpen = open => {
    panel.hidden = !open;
    launcher.setAttribute('aria-expanded', String(open));
    widget.classList.toggle('is-open', open);
    if (open) setTimeout(() => input.focus(), 80);
    else launcher.focus();
  };
  launcher.addEventListener('click', () => setOpen(launcher.getAttribute('aria-expanded') !== 'true'));
  close.addEventListener('click', () => setOpen(false));
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !panel.hidden) setOpen(false);
  });
  widget.querySelectorAll('.rules-widget-prompts button').forEach(prompt => {
    prompt.addEventListener('click', () => {
      input.value = prompt.textContent;
      input.focus();
    });
  });
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      form.requestSubmit();
    }
  });
  connectChat({form, input, messages, status, button: send, getScope: getWidgetScope});
})();
