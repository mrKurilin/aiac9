'use strict';
const panel = document.getElementById('support-chat');
const launcher = document.getElementById('chat-launcher');
const log = document.getElementById('chat-messages');
const input = document.getElementById('chat-input');
const form = document.getElementById('chat-form');
const status = document.getElementById('chat-status');
const send = document.getElementById('chat-send');
const reset = document.getElementById('chat-reset');
const stop = document.getElementById('chat-stop');
let loaded = false;
let controller = null;
function message(role, text) {
  const el = document.createElement('div');
  el.className = 'chat-message ' + role;
  el.textContent = text;
  log.append(el);
  log.scrollTop = log.scrollHeight;
}
function welcome() {
  log.replaceChildren();
  message('assistant', 'Здравствуйте! Я помощник Гараж 33. Проверю наличие запчастей, помогу с доступом к личному кабинету или передам вопрос специалисту.');
  const suggestions = document.createElement('div');
  suggestions.className = 'chat-suggestions';
  ['Есть ли фильтр MANN W 914/2?', 'Есть ли колодки Brembo P 85 020?', 'Не могу войти в личный кабинет'].forEach(text => {
    const button = document.createElement('button');
    button.type = 'button'; button.textContent = text;
    button.addEventListener('click', () => { input.value = text; form.requestSubmit(); });
    suggestions.append(button);
  });
  log.append(suggestions);
}
async function history() {
  try {
    const response = await fetch('/api/chat');
    if (!response.ok) throw new Error();
    const items = await response.json();
    if (items.length) { log.replaceChildren(); items.forEach(item => message(item.role, item.content)); }
    else welcome();
  } catch { welcome(); status.textContent = 'История временно недоступна. Можно задать новый вопрос.'; }
}
async function openChat() {
  panel.hidden = false; launcher.hidden = true; launcher.setAttribute('aria-expanded', 'true');
  if (!loaded) { loaded = true; await history(); }
  input.focus();
}
function closeChat() { panel.hidden = true; launcher.hidden = false; launcher.setAttribute('aria-expanded', 'false'); launcher.focus(); }
document.querySelectorAll('[data-open-chat]').forEach(button => button.addEventListener('click', openChat));
launcher.addEventListener('click', openChat);
document.getElementById('chat-close').addEventListener('click', closeChat);
document.querySelectorAll('[data-question]').forEach(button => button.addEventListener('click', async () => { await openChat(); input.value = button.dataset.question; form.requestSubmit(); }));
panel.addEventListener('keydown', event => { if (event.key === 'Escape') { event.preventDefault(); closeChat(); } });
input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); form.requestSubmit(); } });
form.addEventListener('submit', async event => {
  event.preventDefault();
  const text = input.value.trim();
  if (!text || controller) return;
  message('user', text); input.value = '';
  send.disabled = true; reset.disabled = true; stop.hidden = false;
  status.textContent = 'Проверяем информацию и готовим ответ…';
  controller = new AbortController();
  try {
    const response = await fetch('/api/chat', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ message: text }), signal: controller.signal });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || 'Поддержка временно недоступна.');
    message('assistant', result.answer);
    if (result.task_id) {
      const badge = document.createElement('div'); badge.className = 'task-badge'; badge.textContent = `✓ Обращение #${result.task_id} — передано специалисту`; log.append(badge); log.scrollTop = log.scrollHeight;
    }
    status.textContent = result.resolved ? 'Ответ подготовлен по данным сервиса' : 'Специалист получил контекст вашего вопроса';
  } catch (error) {
    if (error.name === 'AbortError') status.textContent = 'Запрос остановлен.';
    else { message('error', error.message); status.textContent = 'Попробуйте ещё раз или позвоните в автосервис.'; input.value = text; }
  } finally { controller = null; send.disabled = false; reset.disabled = false; stop.hidden = true; input.focus(); }
});
stop.addEventListener('click', () => controller?.abort());
reset.addEventListener('click', async () => {
  if (controller) return;
  reset.disabled = true;
  try {
    const response = await fetch('/api/chat', { method: 'DELETE' });
    if (!response.ok) throw new Error('Не удалось начать новый диалог.');
    welcome(); status.textContent = ''; input.value = ''; input.focus();
  } catch (error) { status.textContent = error.message; }
  finally { reset.disabled = false; }
});
