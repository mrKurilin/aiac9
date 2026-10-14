'use strict';
let tasks = [];
let current = null;
let loading = false;
const dialog = document.getElementById('task-dialog');
const form = document.getElementById('task-form');
const errorBanner = document.getElementById('crm-error');
const detailError = document.getElementById('detail-error');
const search = document.getElementById('task-search');
const categories = { access: 'ДОСТУП', inventory: 'ЗАПЧАСТИ', general: 'ОБРАЩЕНИЕ' };
function el(tag, className, text) { const node = document.createElement(tag); node.className = className; if (text !== undefined) node.textContent = text; return node; }
function showError(error) { errorBanner.textContent = error.message; errorBanner.hidden = false; }
async function api(path, options = {}) {
  const response = await fetch(path, options);
  if (response.status === 401) throw new Error('Сессия сотрудника завершена. Обновите страницу и войдите снова.');
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || 'Не удалось выполнить действие.');
  return result;
}
function date(value) { return new Date(value).toLocaleString('ru-RU', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }); }
function render() {
  const query = search.value.trim().toLowerCase();
  const filtered = tasks.filter(task => [task.title, task.phone, task.reason, '#' + task.id].join(' ').toLowerCase().includes(query));
  document.getElementById('total-count').textContent = tasks.length;
  document.querySelectorAll('.column').forEach(column => {
    const items = filtered.filter(task => task.status === column.dataset.status);
    column.querySelector('.column-count').textContent = items.length;
    const list = column.querySelector('.task-list'); list.replaceChildren();
    if (!items.length) list.append(el('div', 'empty-column', query ? 'Ничего не найдено' : column.dataset.status === 'TODO' ? 'Новые вопросы из чата\nпоявятся здесь' : column.dataset.status === 'DONE' ? 'Здесь будут\nрешённые обращения' : 'Здесь будут задачи,\nкоторые взяли в работу'));
    items.forEach(task => {
      const card = el('button', 'task-card'); card.type = 'button'; card.draggable = true; card.dataset.taskId = task.id;
      card.setAttribute('aria-label', `Задача ${task.id}: ${task.title}`);
      const top = el('div', 'task-card-top'); top.append(el('span', 'category-tag ' + task.category, categories[task.category]), el('span', '', '#' + String(task.id).padStart(3, '0')));
      const footer = el('div', 'task-card-footer'); footer.append(el('span', 'task-phone', task.phone ? '+' + task.phone : task.session_id ? 'Из чата поддержки' : 'Создана сотрудником'), el('span', '', date(task.created_at)));
      card.append(top, el('h3', '', task.title), el('p', '', task.reason), footer);
      card.addEventListener('click', () => openTask(task));
      card.addEventListener('dragstart', event => { event.dataTransfer.setData('text/plain', String(task.id)); event.dataTransfer.effectAllowed = 'move'; });
      list.append(card);
    });
  });
}
async function refresh() {
  if (loading) return;
  loading = true;
  try { tasks = await api('/api/crm/tasks'); errorBanner.hidden = true; render(); document.getElementById('sync-status').textContent = 'Обновлено ' + new Date().toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }); }
  catch (error) { showError(error); }
  finally { loading = false; }
}
async function openTask(task) {
  if (task) {
    try { task = await api('/api/crm/tasks/' + task.id); }
    catch (error) { showError(error); return; }
  }
  current = task;
  document.getElementById('task-label').textContent = task ? `ОБРАЩЕНИЕ #${task.id} / ${categories[task.category]}` : 'НОВАЯ ЗАДАЧА';
  document.getElementById('task-title').value = task?.title || '';
  document.getElementById('task-reason').value = task?.reason || '';
  document.getElementById('task-status').value = task?.status || 'TODO';
  document.getElementById('task-status').disabled = !task;
  document.getElementById('task-phone').textContent = task?.phone ? '+' + task.phone : 'Не указан';
  document.getElementById('task-date').textContent = task ? 'Создана ' + date(task.created_at) : 'Задача появится в TODO';
  document.getElementById('task-context').hidden = !task;
  const code = document.getElementById('task-code'); code.replaceChildren();
  (task?.code_hints || []).forEach(hint => code.append(el('li', '', hint)));
  const sources = document.getElementById('task-sources'); sources.replaceChildren();
  (task?.sources || []).forEach(source => sources.append(el('span', '', source)));
  if (!sources.childNodes.length) sources.append(el('span', '', 'Не использовалась'));
  const transcript = document.getElementById('task-transcript'); transcript.replaceChildren();
  (task?.messages || []).forEach(record => { const item = el('div', record.role); item.append(el('small', '', (record.role === 'user' ? 'КЛИЕНТ' : 'ПОДДЕРЖКА') + ' · ' + date(record.created_at)), document.createTextNode(record.content)); transcript.append(item); });
  if (!transcript.childNodes.length) transcript.append(el('div', '', 'Создана сотрудником без диалога.'));
  detailError.hidden = true;
  dialog.showModal();
}
document.querySelectorAll('.column').forEach(column => {
  column.addEventListener('dragover', event => { event.preventDefault(); column.classList.add('drag-over'); });
  column.addEventListener('dragleave', event => { if (!column.contains(event.relatedTarget)) column.classList.remove('drag-over'); });
  column.addEventListener('drop', async event => {
    event.preventDefault(); column.classList.remove('drag-over');
    const task = tasks.find(item => String(item.id) === event.dataTransfer.getData('text/plain'));
    if (!task || task.status === column.dataset.status) return;
    try { await api('/api/crm/tasks/' + task.id, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: task.title, reason: task.reason, status: column.dataset.status }) }); await refresh(); }
    catch (error) { showError(error); }
  });
});
form.addEventListener('submit', async event => {
  event.preventDefault();
  const save = document.getElementById('task-save'); save.disabled = true;
  const payload = { title: document.getElementById('task-title').value, reason: document.getElementById('task-reason').value };
  if (current) payload.status = document.getElementById('task-status').value;
  try { await api('/api/crm/tasks' + (current ? '/' + current.id : ''), { method: current ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }); dialog.close(); await refresh(); }
  catch (error) { detailError.textContent = error.message; detailError.hidden = false; }
  finally { save.disabled = false; }
});
document.getElementById('detail-close').addEventListener('click', () => dialog.close());
document.getElementById('new-task').addEventListener('click', () => openTask(null));
document.querySelector('.add-in-column').addEventListener('click', () => openTask(null));
document.getElementById('refresh').addEventListener('click', refresh);
search.addEventListener('input', render);
refresh();
setInterval(() => { if (!dialog.open && !document.hidden) refresh(); }, 10000);
