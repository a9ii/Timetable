// =============================================================================
// app.js — Intelligent Timetable System — Vanilla JS ES6 Module
// =============================================================================

const API = '';  // same-origin

// =============================================================================
// STATE
// =============================================================================
const State = {
  teachers:   new Map(),
  classes:    new Map(),
  subjects:   new Map(),
  classrooms: new Map(),
  periods:    [],
  days:       [],
  lessons:    [],
  cards:      [],
};

let crudContext  = { resource: '', editId: null };
let editMode     = false;   // drag-and-drop editing toggle
let dragCardId   = null;    // id of the card currently being dragged

// =============================================================================
// UTILITIES
// =============================================================================
function toast(msg, type = 'info') {
  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.textContent = msg;
  document.getElementById('toast-container').appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

async function apiFetch(path, opts = {}) {
  const res = await fetch(API + path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }));
    throw new Error(err.message || 'Request failed');
  }
  return res.json();
}

function hex2rgba(hex, a = 0.18) {
  const r = parseInt(hex.slice(1,3),16);
  const g = parseInt(hex.slice(3,5),16);
  const b = parseInt(hex.slice(5,7),16);
  return `rgba(${r},${g},${b},${a})`;
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}


// =============================================================================
// BOOTSTRAP — load all master data on page load
// =============================================================================
export const App = { loadCards };

async function bootstrap() {
  try {
    const [teachers, classes, subjects, classrooms, periods, days, lessons] = await Promise.all([
      apiFetch('/api/teachers'),
      apiFetch('/api/classes'),
      apiFetch('/api/subjects'),
      apiFetch('/api/classrooms'),
      apiFetch('/api/periods'),
      apiFetch('/api/days'),
      apiFetch('/api/lessons'),
    ]);

    // Populate Maps for O(1) lookup
    State.teachers.clear();
    teachers.forEach(t => State.teachers.set(t.id, t));
    State.classes.clear();
    classes.forEach(c => State.classes.set(c.id, c));
    State.subjects.clear();
    subjects.forEach(s => State.subjects.set(s.id, s));
    State.classrooms.clear();
    classrooms.forEach(r => State.classrooms.set(r.id, r));

    State.periods = periods.sort((a,b) => a.index - b.index);
    State.days    = days.sort((a,b) => a.index - b.index);
    State.lessons = lessons;

    // Populate tables
    renderTeachersTable(teachers);
    renderClassesTable(classes);
    renderSubjectsTable(subjects);
    renderClassroomsTable(classrooms);
    renderPeriodsTable(periods);
    renderDaysTable(days);
    renderLessonsTable(lessons);

    // Populate class filter dropdown
    const sel = document.getElementById('filter-class');
    sel.innerHTML = '<option value="">All Classes</option>';
    classes.forEach(c => {
      const opt = document.createElement('option');
      opt.value = c.id; opt.textContent = c.name;
      sel.appendChild(opt);
    });

    await loadCards();
    toast('Data loaded successfully', 'success');
  } catch(e) {
    toast('Failed to load data: ' + e.message, 'error');
  }
}

async function loadCards() {
  try {
    const classId = document.getElementById('filter-class')?.value || '';
    const qs = classId ? `?classId=${classId}` : '';
    State.cards = await apiFetch('/api/cards' + qs);
    renderTimetable();
  } catch(e) {
    toast('Failed to load cards: ' + e.message, 'error');
  }
}

// expose globally for HTML onclick
window.App = { loadCards };

// =============================================================================
// EDIT MODE TOGGLE
// =============================================================================
function toggleEditMode() {
  editMode = !editMode;
  const label = document.getElementById('edit-mode-label');
  const btn   = document.getElementById('btn-edit-mode');
  if (editMode) {
    label.textContent = 'Done Editing';
    btn.style.background = 'rgba(99,102,241,.3)';
    btn.style.borderColor = 'rgba(99,102,241,.7)';
    btn.style.color = '#c7d2fe';
    toast('✏️ Edit mode ON — drag cards to reschedule', 'info');
  } else {
    label.textContent = 'Edit Mode';
    btn.style.background = '';
    btn.style.borderColor = '';
    btn.style.color = '';
    toast('Edit mode OFF', 'info');
  }
  renderTimetable(); // re-render with/without draggable
}
window.toggleEditMode = toggleEditMode;

// =============================================================================
// MOVE CARD (PATCH with constraint check)
// =============================================================================
async function moveCard(cardId, newDayId, newPeriodId, targetCell) {
  try {
    const updated = await apiFetch(`/api/cards/${cardId}`, {
      method: 'PATCH',
      body: JSON.stringify({ periodId: newPeriodId, dayDefId: newDayId }),
    });

    // Update in-memory cards array
    const idx = State.cards.findIndex(c => c.id === cardId);
    if (idx !== -1) State.cards[idx] = updated;

    // Flash the target cell green
    targetCell.classList.add('drop-ok');
    setTimeout(() => targetCell.classList.remove('drop-ok'), 450);

    renderTimetable();
    toast('✅ Card moved successfully', 'success');
  } catch(e) {
    // Flash the target cell red
    targetCell.classList.add('drop-err');
    setTimeout(() => targetCell.classList.remove('drop-err'), 450);
    toast(e.message, 'error');
  }
}

// =============================================================================
// TIMETABLE GRID RENDERER  (with drag-and-drop support)
// =============================================================================
function renderTimetable() {
  const grid    = document.getElementById('timetable-grid');
  const empty   = document.getElementById('timetable-empty');
  const days    = State.days;
  const periods = State.periods;

  if (!days.length || !periods.length) {
    grid.innerHTML = ''; empty.classList.remove('hidden'); return;
  }

  // Build lookup: Map<dayId|periodId, Card[]>
  const cardMap = new Map();
  State.cards.forEach(card => {
    const key = `${card.dayDefId}|${card.periodId}`;
    if (!cardMap.has(key)) cardMap.set(key, []);
    cardMap.get(key).push(card);
  });

  if (State.cards.length === 0) {
    empty.classList.remove('hidden'); grid.innerHTML = ''; return;
  }
  empty.classList.add('hidden');

  grid.style.gridTemplateColumns = `60px repeat(${periods.length}, minmax(110px, 1fr))`;

  let html = '';

  // Header row
  html += '<div></div>';
  periods.forEach(p => {
    html += `<div class="grid-header-cell">
      <div class="font-bold">${escHtml(p.name)}</div>
      <div class="text-[.62rem] opacity-60 mt-0.5">${escHtml(p.startTime)}–${escHtml(p.endTime)}</div>
    </div>`;
  });

  // Day rows
  days.forEach(day => {
    html += `<div class="grid-day-label">${escHtml(day.name.substring(0,3).toUpperCase())}</div>`;
    periods.forEach(period => {
      const key   = `${day.id}|${period.id}`;
      const cards = cardMap.get(key) || [];

      // Each cell carries its day/period as data attributes for the drop handler
      html += `<div class="grid-cell"
        data-day="${escHtml(day.id)}"
        data-period="${escHtml(period.id)}"
        ${editMode ? 'ondragover="cellDragOver(event)" ondragleave="cellDragLeave(event)" ondrop="cellDrop(event)"' : ''}
      >`;

      cards.forEach(card => { html += buildCardChip(card); });
      html += '</div>';
    });
  });

  grid.innerHTML = html;
}

function buildCardChip(card) {
  const lesson      = card.lesson   || {};
  const subject     = lesson.subject || {};
  const subjectName = escHtml(subject.name || '—');

  const teacherNames = (lesson.teacherIds || [])
    .map(id => State.teachers.get(id)?.shortCode || '?').join(', ');
  const classNames = (lesson.classIds || [])
    .map(id => State.classes.get(id)?.name || '?').join(' + ');
  const roomName = card.classroomId
    ? (State.classrooms.get(card.classroomId)?.name || '') : '';

  const firstTeacher = (lesson.teacherIds || [])[0];
  const color = State.teachers.get(firstTeacher)?.color || '#6366f1';
  const bg    = hex2rgba(color, 0.22);
  const bdr   = hex2rgba(color, 0.40);

  // In edit mode: make the chip draggable and attach dragstart/dragend
  const draggable = editMode
    ? `draggable="true" ondragstart="chipDragStart(event,'${card.id}')" ondragend="chipDragEnd(event)"`
    : '';

  return `<div class="card-chip" ${draggable}
    data-card-id="${card.id}"
    style="background:${bg};border:1px solid ${bdr};"
    title="${escHtml(editMode ? 'Drag to reschedule' : subjectName)}">
    <div class="subject" style="color:${color};">${subjectName}</div>
    <div class="teacher">${escHtml(teacherNames)}</div>
    <div class="class">${escHtml(classNames)}</div>
    ${roomName ? `<div class="room">🚪 ${escHtml(roomName)}</div>` : ''}
    ${editMode ? '<div class="room" style="color:#6366f1;opacity:.7;">⠿ drag</div>' : ''}
  </div>`;
}

// expose for filter dropdown onchange
window.renderTimetable = renderTimetable;

// =============================================================================
// DRAG-AND-DROP EVENT HANDLERS (attached via inline HTML in editMode)
// =============================================================================

function chipDragStart(event, cardId) {
  dragCardId = cardId;
  // Defer adding .dragging so the drag image captures the original style
  requestAnimationFrame(() => {
    event.target.classList.add('dragging');
  });
  event.dataTransfer.effectAllowed = 'move';
  event.dataTransfer.setData('text/plain', cardId);
}
window.chipDragStart = chipDragStart;

function chipDragEnd(event) {
  dragCardId = null;
  document.querySelectorAll('.card-chip.dragging').forEach(el => el.classList.remove('dragging'));
  document.querySelectorAll('.grid-cell.drag-over').forEach(el => el.classList.remove('drag-over'));
}
window.chipDragEnd = chipDragEnd;

function cellDragOver(event) {
  event.preventDefault();
  event.dataTransfer.dropEffect = 'move';
  // Highlight the cell being hovered
  const cell = event.currentTarget;
  cell.classList.add('drag-over');
}
window.cellDragOver = cellDragOver;

function cellDragLeave(event) {
  // Only remove if we've genuinely left the cell (not just entered a child)
  const cell = event.currentTarget;
  if (!cell.contains(event.relatedTarget)) {
    cell.classList.remove('drag-over');
  }
}
window.cellDragLeave = cellDragLeave;

function cellDrop(event) {
  event.preventDefault();
  const cell     = event.currentTarget;
  cell.classList.remove('drag-over');

  const cardId   = dragCardId || event.dataTransfer.getData('text/plain');
  const newDay   = cell.dataset.day;
  const newPeriod= cell.dataset.period;

  if (!cardId || !newDay || !newPeriod) return;

  // Don't move a card onto the same cell it already occupies
  const card = State.cards.find(c => c.id === cardId);
  if (card && card.dayDefId === newDay && card.periodId === newPeriod) {
    toast('Card is already in this slot', 'info');
    return;
  }

  moveCard(cardId, newDay, newPeriod, cell);
}
window.cellDrop = cellDrop;



// =============================================================================
// TABLE RENDERERS
// =============================================================================
function renderTeachersTable(list) {
  const tbody = document.getElementById('teachers-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(t => `
    <tr class="data-row">
      <td><span class="color-dot" style="background:${t.color || '#6366f1'}"></span></td>
      <td class="font-medium text-white">${escHtml(t.name)}</td>
      <td class="text-slate-400">${escHtml(t.shortCode)}</td>
      <td class="text-slate-400">${t.maxPeriodsPerWeek}</td>
      <td class="text-slate-400 text-xs">${escHtml((t.availableDayIds || []).join(', ') || 'All Days')}</td>
      <td class="flex gap-2">
        <button onclick='openModal("teacher",${JSON.stringify(t).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("teachers","${t.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderClassesTable(list) {
  const tbody = document.getElementById('classes-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(c => `
    <tr class="data-row">
      <td class="font-medium text-white">${escHtml(c.name)}</td>
      <td class="text-slate-400">${c.capacity}</td>
      <td class="text-slate-400">${escHtml(c.shift || 'Any')}</td>
      <td class="flex gap-2">
        <button onclick='openModal("class",${JSON.stringify(c).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("classes","${c.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderSubjectsTable(list) {
  const tbody = document.getElementById('subjects-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(s => `
    <tr class="data-row">
      <td class="font-medium text-white">${escHtml(s.name)}</td>
      <td><span class="badge ${s.requiresLab ? 'badge-lab' : 'badge-normal'}">${s.requiresLab ? '🔬 Lab' : '📖 Normal'}</span></td>
      <td class="flex gap-2">
        <button onclick='openModal("subject",${JSON.stringify(s).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("subjects","${s.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderClassroomsTable(list) {
  const tbody = document.getElementById('classrooms-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(r => `
    <tr class="data-row">
      <td class="font-medium text-white">${escHtml(r.name)}</td>
      <td><span class="badge ${r.isLab ? 'badge-lab' : 'badge-normal'}">${r.isLab ? '🔬 Lab' : '🚪 Room'}</span></td>
      <td class="text-slate-400">${r.capacity}</td>
      <td class="flex gap-2">
        <button onclick='openModal("classroom",${JSON.stringify(r).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("classrooms","${r.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderPeriodsTable(list) {
  const tbody = document.getElementById('periods-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(p => `
    <tr class="data-row">
      <td class="text-slate-500 font-mono text-xs">${escHtml(p.id)}</td>
      <td class="font-medium text-white">${escHtml(p.name)}</td>
      <td class="text-slate-400">${escHtml(p.startTime)}</td>
      <td class="text-slate-400">${escHtml(p.endTime)}</td>
      <td class="text-slate-400">${escHtml(p.shift || 'Any')}</td>
      <td class="text-slate-500">${p.index}</td>
      <td class="flex gap-2">
        <button onclick='openModal("period",${JSON.stringify(p).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("periods","${p.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderDaysTable(list) {
  const tbody = document.getElementById('days-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(d => `
    <tr class="data-row">
      <td class="text-slate-500 font-mono text-xs">${escHtml(d.id)}</td>
      <td class="font-medium text-white">${escHtml(d.name)}</td>
      <td class="font-mono text-slate-500 text-xs">${escHtml(d.binaryValue)}</td>
      <td class="text-slate-500">${d.index}</td>
      <td class="flex gap-2">
        <button onclick='openModal("day",${JSON.stringify(d).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("days","${d.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`).join('');
}

function renderLessonsTable(list) {
  const tbody = document.getElementById('lessons-body');
  if (!tbody) return;
  tbody.innerHTML = list.map(l => {
    const sub  = l.subject?.name || State.subjects.get(l.subjectId)?.name || '—';
    const tchs = (l.teacherIds || []).map(id => State.teachers.get(id)?.name || id).join(', ');
    const cls  = (l.classIds  || []).map(id => State.classes.get(id)?.name  || id).join(', ');
    return `<tr class="data-row">
      <td class="font-medium text-white">${escHtml(sub)}</td>
      <td class="text-slate-400 text-xs">${escHtml(tchs)}</td>
      <td class="text-slate-400 text-xs">${escHtml(cls)}</td>
      <td class="text-center"><span class="badge badge-normal">${l.countPerWeek}×/wk</span></td>
      <td class="flex gap-2">
        <button onclick='openModal("lesson",${JSON.stringify(l).replace(/'/g,"&#39;")})' class="text-xs text-indigo-400 hover:text-indigo-300">Edit</button>
        <button onclick='deleteRecord("lessons","${l.id}")' class="text-xs text-red-400 hover:text-red-300">Del</button>
      </td>
    </tr>`;
  }).join('');
}

// =============================================================================
// GENERIC DELETE
// =============================================================================
async function deleteRecord(resource, id) {
  if (!confirm(`Delete this ${resource.slice(0,-1)}?`)) return;
  try {
    await apiFetch(`/api/${resource}/${id}`, { method: 'DELETE' });
    toast('Deleted successfully', 'success');
    bootstrap();
  } catch(e) {
    toast('Delete failed: ' + e.message, 'error');
  }
}
window.deleteRecord = deleteRecord;

// =============================================================================
// CRUD MODAL
// =============================================================================
const FORMS = {
  teacher: {
    title: 'Teacher', endpoint: 'teachers',
    fields: [
      { name:'name', label:'Full Name', type:'text', required:true },
      { name:'shortCode', label:'Short Code', type:'text', required:true, maxlength:10 },
      { name:'maxPeriodsPerWeek', label:'Max Periods / Week', type:'number', required:true, value:20 },
      { name:'availableDayIds', label:'Available Days (leave empty for all)', type:'multi-day' },
      { name:'color', label:'Color', type:'color', value:'#6366f1' },
    ]
  },
  class: {
    title: 'Class', endpoint: 'classes',
    fields: [
      { name:'name', label:'Class Name', type:'text', required:true },
      { name:'capacity', label:'Capacity', type:'number', required:true, value:30 },
      { name:'shift', label:'Shift', type:'select-shift' },
    ]
  },
  subject: {
    title: 'Subject', endpoint: 'subjects',
    fields: [
      { name:'name', label:'Subject Name', type:'text', required:true },
      { name:'requiresLab', label:'Requires Lab Room', type:'checkbox' },
    ]
  },
  classroom: {
    title: 'Classroom', endpoint: 'classrooms',
    fields: [
      { name:'name', label:'Room Name', type:'text', required:true },
      { name:'isLab', label:'Is a Lab Room', type:'checkbox' },
      { name:'capacity', label:'Capacity', type:'number', required:true, value:35 },
    ]
  },
  period: {
    title: 'Period', endpoint: 'periods',
    fields: [
      { name:'id', label:'ID (e.g. P1)', type:'text', required:true, maxlength:20 },
      { name:'name', label:'Display Name', type:'text', required:true },
      { name:'startTime', label:'Start Time (HH:MM)', type:'time', required:true },
      { name:'endTime',   label:'End Time (HH:MM)',   type:'time', required:true },
      { name:'shift', label:'Shift', type:'select-shift' },
      { name:'index', label:'Sort Index', type:'number', value:0 },
    ]
  },
  day: {
    title: 'Day', endpoint: 'days',
    fields: [
      { name:'id', label:'ID (e.g. MON)', type:'text', required:true, maxlength:20 },
      { name:'name', label:'Day Name', type:'text', required:true },
      { name:'binaryValue', label:'Binary Value', type:'text', value:'10000', maxlength:10 },
      { name:'index', label:'Sort Index', type:'number', value:0 },
    ]
  },
  lesson: {
    title: 'Lesson', endpoint: 'lessons',
    fields: [
      { name:'subjectId', label:'Subject', type:'select-subject', required:true },
      { name:'teacherIds', label:'Teachers (multi)', type:'multi-teacher', required:true },
      { name:'classIds',   label:'Classes (multi)',  type:'multi-class',   required:true },
      { name:'countPerWeek', label:'Periods per Week', type:'number', required:true, value:2 },
    ]
  },
};

function buildField(f, data) {
  const val = data?.[f.name];
  const id  = 'field-' + f.name;

  if (f.type === 'checkbox') {
    const checked = val ? 'checked' : '';
    return `<label class="flex items-center gap-3 cursor-pointer">
      <input id="${id}" name="${f.name}" type="checkbox" ${checked}
        class="w-4 h-4 rounded accent-indigo-500" />
      <span class="form-label !mb-0 !normal-case !tracking-normal">${f.label}</span>
    </label>`;
  }
  if (f.type === 'select-subject') {
    const opts = [...State.subjects.values()].map(s =>
      `<option value="${s.id}" ${val===s.id?'selected':''}>${escHtml(s.name)}</option>`).join('');
    return `<div><label class="form-label" for="${id}">${f.label}</label>
      <select id="${id}" name="${f.name}" class="form-input" required><option value="">— Select —</option>${opts}</select></div>`;
  }
  if (f.type === 'multi-teacher') {
    const selected = new Set(val || []);
    const opts = [...State.teachers.values()].map(t =>
      `<option value="${t.id}" ${selected.has(t.id)?'selected':''}>${escHtml(t.name)}</option>`).join('');
    return `<div><label class="form-label" for="${id}">${f.label}</label>
      <select id="${id}" name="${f.name}" multiple class="form-input h-28">${opts}</select></div>`;
  }
  if (f.type === 'multi-class') {
    const selected = new Set(val || []);
    const opts = [...State.classes.values()].map(c =>
      `<option value="${c.id}" ${selected.has(c.id)?'selected':''}>${escHtml(c.name)}</option>`).join('');
    return `<div><label class="form-label" for="${id}">${f.label}</label>
      <select id="${id}" name="${f.name}" multiple class="form-input h-28">${opts}</select></div>`;
  }
  if (f.type === 'multi-day') {
    const selected = new Set(val || []);
    const opts = [...State.days].map(d =>
      `<option value="${d.id}" ${selected.has(d.id)?'selected':''}>${escHtml(d.name)}</option>`).join('');
    return `<div><label class="form-label" for="${id}">${f.label}</label>
      <select id="${id}" name="${f.name}" multiple class="form-input h-28">${opts}</select></div>`;
  }
  if (f.type === 'select-shift') {
    return `<div><label class="form-label" for="${id}">${f.label}</label>
      <select id="${id}" name="${f.name}" class="form-input">
        <option value="" ${!val?'selected':''}>Any / None</option>
        <option value="morning" ${val==='morning'?'selected':''}>Morning</option>
        <option value="evening" ${val==='evening'?'selected':''}>Evening</option>
      </select></div>`;
  }

  const attrs = [
    f.required  ? 'required'              : '',
    f.maxlength ? `maxlength="${f.maxlength}"` : '',
  ].join(' ');
  const curVal = val !== undefined ? val : (f.value !== undefined ? f.value : '');
  return `<div><label class="form-label" for="${id}">${f.label}</label>
    <input id="${id}" name="${f.name}" type="${f.type}" value="${escHtml(String(curVal))}"
      class="form-input" ${attrs} /></div>`;
}

function openModal(type, data = null) {
  const def = FORMS[type];
  if (!def) return;
  crudContext = { resource: type, endpoint: def.endpoint, editId: data?.id || null };
  document.getElementById('crud-title').textContent = (data ? 'Edit ' : 'Add ') + def.title;
  document.getElementById('crud-fields').innerHTML = def.fields.map(f => buildField(f, data)).join('');
  document.getElementById('crud-modal').classList.remove('hidden');
}
window.openModal = openModal;

function closeCrudModal() {
  document.getElementById('crud-modal').classList.add('hidden');
}
window.closeCrudModal = closeCrudModal;

async function submitCrudForm(e) {
  e.preventDefault();
  const { endpoint, editId } = crudContext;
  const def = FORMS[crudContext.resource];
  const body = {};

  def.fields.forEach(f => {
    const el = document.getElementById('field-' + f.name);
    if (!el) return;
    if (f.type === 'checkbox')      body[f.name] = el.checked;
    else if (f.type === 'multi-teacher' || f.type === 'multi-class' || f.type === 'multi-day') {
      body[f.name] = [...el.selectedOptions].map(o => o.value);
    }
    else if (f.type === 'number')   body[f.name] = Number(el.value);
    else                             body[f.name] = el.value;
  });

  try {
    const method = editId ? 'PUT' : 'POST';
    const url    = editId ? `/api/${endpoint}/${editId}` : `/api/${endpoint}`;
    await apiFetch(url, { method, body: JSON.stringify(body) });
    toast(`${def.title} saved successfully`, 'success');
    closeCrudModal();
    bootstrap();
  } catch(e) {
    toast('Save failed: ' + e.message, 'error');
  }
}
window.submitCrudForm = submitCrudForm;

// =============================================================================
// GENERATE — trigger solver and open progress modal
// =============================================================================
async function triggerGenerate() {
  openProgressModal();
  let jobId;
  try {
    const res = await apiFetch('/api/generate', { method: 'POST' });
    jobId = res.jobId;
    appendLog(`🔗 Job ID: ${jobId}`);
  } catch(e) {
    setProgressFailed('Could not start solver: ' + e.message);
    return;
  }

  // Connect WebSocket
  const wsProto = location.protocol === 'https:' ? 'wss' : 'ws';
  const wsHost  = location.host || 'localhost:8080';
  const ws = new WebSocket(`${wsProto}://${wsHost}/ws/${jobId}`);

  ws.onopen = () => appendLog('📡 WebSocket connected — streaming progress...');

  ws.onmessage = (evt) => {
    let msg;
    try { msg = JSON.parse(evt.data); } catch { return; }

    setProgress(msg.percentage, msg.message);
    if (msg.message) appendLog(msg.message);

    if (msg.type === 'completed') {
      setProgressDone(msg.message);
      loadCards();
      ws.close();
    } else if (msg.type === 'failed') {
      setProgressFailed(msg.message);
      ws.close();
    }
  };

  ws.onerror = () => setProgressFailed('WebSocket connection error.');
  ws.onclose = () => appendLog('🔌 WebSocket closed.');
}
window.triggerGenerate = triggerGenerate;

// --- Progress Modal helpers ---
function openProgressModal() {
  const modal = document.getElementById('progress-modal');
  document.getElementById('progress-fill').style.width = '0%';
  document.getElementById('progress-pct').textContent  = '0%';
  document.getElementById('progress-msg').textContent  = 'Initialising...';
  document.getElementById('progress-subtitle').textContent = 'CSP Backtracking Solver';
  document.getElementById('progress-log').innerHTML    = '';
  document.getElementById('icon-spinner').classList.remove('hidden');
  document.getElementById('icon-done').classList.add('hidden');
  document.getElementById('icon-fail').classList.add('hidden');
  document.getElementById('btn-close-progress').disabled = true;
  document.getElementById('btn-view-timetable').classList.add('hidden');
  modal.classList.remove('hidden');
}

function setProgress(pct, msg) {
  document.getElementById('progress-fill').style.width = `${pct}%`;
  document.getElementById('progress-pct').textContent  = `${pct}%`;
  if (msg) document.getElementById('progress-msg').textContent = msg;
}

function appendLog(line) {
  const log = document.getElementById('progress-log');
  const el  = document.createElement('div');
  el.textContent = '› ' + line;
  log.appendChild(el);
  log.scrollTop  = log.scrollHeight;
}

function setProgressDone(msg) {
  document.getElementById('icon-spinner').classList.add('hidden');
  document.getElementById('icon-done').classList.remove('hidden');
  document.getElementById('progress-subtitle').textContent = '✅ Completed';
  document.getElementById('btn-close-progress').disabled   = false;
  document.getElementById('btn-view-timetable').classList.remove('hidden');
  setProgress(100, msg);
}

function setProgressFailed(msg) {
  document.getElementById('icon-spinner').classList.add('hidden');
  document.getElementById('icon-fail').classList.remove('hidden');
  document.getElementById('progress-subtitle').textContent = '❌ Failed';
  document.getElementById('btn-close-progress').disabled   = false;
  appendLog('ERROR: ' + msg);
}

function closeProgressModal() {
  document.getElementById('progress-modal').classList.add('hidden');
}
window.closeProgressModal = closeProgressModal;

// =============================================================================
// INIT
// =============================================================================
document.addEventListener('DOMContentLoaded', bootstrap);
