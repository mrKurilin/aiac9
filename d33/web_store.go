package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

type crmStore struct {
	db  *sql.DB
	now func() time.Time
}

type chatRecord struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type crmTask struct {
	ID        int64        `json:"id"`
	Title     string       `json:"title"`
	Status    string       `json:"status"`
	Category  string       `json:"category"`
	Phone     string       `json:"phone"`
	Reason    string       `json:"reason"`
	SessionID string       `json:"session_id"`
	CodeHints []string     `json:"code_hints"`
	Sources   []string     `json:"sources"`
	Messages  []chatRecord `json:"messages"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
}

type inventoryItem struct {
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Price    int    `json:"price_rub"`
}

type accessResult struct {
	Phone              string `json:"phone"`
	Allowed            bool   `json:"allowed"`
	Reason             string `json:"reason"`
	BlockedDate        string `json:"blocked_date,omitempty"`
	RetryDate          string `json:"retry_date,omitempty"`
	NeedsInvestigation bool   `json:"needs_investigation"`
}

func openCRMStore(path string, now func() time.Time) (*crmStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(abs)+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &crmStore{db: db, now: now}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *crmStore) initialize() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS inventory (sku TEXT PRIMARY KEY, name TEXT NOT NULL, quantity INTEGER NOT NULL, price INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS accounts (phone TEXT PRIMARY KEY, state TEXT NOT NULL, blocked_date TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS messages (id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS messages_session ON messages(session_id, id);
CREATE TABLE IF NOT EXISTS tasks (
 id INTEGER PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('TODO','IN PROGRESS','DONE')),
 category TEXT NOT NULL, phone TEXT NOT NULL, reason TEXT NOT NULL, session_id TEXT NOT NULL,
 code_hints TEXT NOT NULL, sources TEXT NOT NULL, transcript TEXT NOT NULL,
 dedupe_key TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS active_task ON tasks(dedupe_key) WHERE status != 'DONE';
INSERT OR IGNORE INTO inventory VALUES ('MANN W 914/2','Масляный фильтр MANN W 914/2',8,790);
INSERT OR IGNORE INTO inventory VALUES ('Brembo P 85 020','Тормозные колодки Brembo P 85 020',0,4200);
INSERT OR IGNORE INTO accounts(phone,state) VALUES ('79000000002','unexplained_failure'),('79000000003','active');`)
	if err != nil {
		return err
	}
	// The date is seeded once. Restarting tomorrow must not extend today's outage.
	_, err = s.db.Exec(`INSERT OR IGNORE INTO accounts(phone,state,blocked_date) VALUES (?,?,?)`, "79000000001", "daily_maintenance", s.now().Format("2006-01-02"))
	return err
}

func normalizePhone(value string) (string, error) {
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else if !strings.ContainsRune("+()- ", r) {
			return "", fmt.Errorf("укажите российский номер телефона из 11 цифр")
		}
	}
	phone := digits.String()
	if len(phone) == 11 && phone[0] == '8' {
		phone = "7" + phone[1:]
	}
	if len(phone) != 11 || phone[0] != '7' {
		return "", fmt.Errorf("укажите российский номер телефона из 11 цифр")
	}
	return phone, nil
}

func accessDecision(phone, state, blockedDate string, today time.Time) accessResult {
	result := accessResult{Phone: phone}
	switch state {
	case "active":
		result.Allowed, result.Reason = true, "Доступ к личному кабинету открыт."
	case "daily_maintenance":
		if blockedDate == today.Format("2006-01-02") {
			result.BlockedDate = blockedDate
			result.RetryDate = today.AddDate(0, 0, 1).Format("2006-01-02")
			result.Reason = "Сегодня доступ временно ограничен из-за обслуживания. Доступ восстановится после полуночи по Москве; пароль менять не требуется."
		} else {
			result.Allowed, result.Reason = true, "Временное ограничение закончилось; доступ открыт."
		}
	case "unexplained_failure":
		result.NeedsInvestigation = true
		result.Reason = "Вход не работает, подтверждённой причины нет. Требуется проверка специалистом."
	default:
		result.NeedsInvestigation = true
		result.Reason = "Номер не найден в базе; требуется проверка регистрации специалистом."
	}
	return result
}

func (s *crmStore) lookupAccess(ctx context.Context, value string) (accessResult, error) {
	phone, err := normalizePhone(value)
	if err != nil {
		return accessResult{}, err
	}
	var state, date string
	err = s.db.QueryRowContext(ctx, `SELECT state,blocked_date FROM accounts WHERE phone=?`, phone).Scan(&state, &date)
	if err != nil && err != sql.ErrNoRows {
		return accessResult{}, err
	}
	return accessDecision(phone, state, date, s.now()), nil
}

func (s *crmStore) inventory(ctx context.Context) ([]inventoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sku,name,quantity,price FROM inventory ORDER BY sku`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []inventoryItem{}
	for rows.Next() {
		var item inventoryItem
		if err := rows.Scan(&item.SKU, &item.Name, &item.Quantity, &item.Price); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *crmStore) appendMessage(ctx context.Context, session, role, content string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages(session_id,role,content,created_at) VALUES (?,?,?,?)`, session, role, content, s.now().Format(time.RFC3339))
	return err
}

func (s *crmStore) conversation(ctx context.Context, session string) ([]chatRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role,content,created_at FROM (SELECT id,role,content,created_at FROM messages WHERE session_id=? ORDER BY id DESC LIMIT 60) ORDER BY id`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []chatRecord{}
	for rows.Next() {
		var record chatRecord
		if err := rows.Scan(&record.Role, &record.Content, &record.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	start, size := len(records), 0
	for start > 0 {
		next := len([]rune(records[start-1].Content))
		if size+next > 24000 {
			break
		}
		size += next
		start--
	}
	return records[start:], nil
}

func investigationHints(category string) []string {
	switch category {
	case "access":
		return []string{
			"aiac9/d33/web_store.go → normalizePhone: проверить нормализацию +7 / 8 и поиск номера.",
			"aiac9/d33/web_store.go → lookupAccess / accessDecision: проверить state, blocked_date и московскую дату; не продлевать ограничение при перезапуске.",
			"SQLite accounts: сверить запись phone и состояние unexplained_failure; в учебной базе причина намеренно неизвестна.",
		}
	case "inventory":
		return []string{
			"aiac9/d33/crm_http.go → handleMCP, inventory_list: проверить данные, возвращаемые MCP.",
			"aiac9/d33/web_store.go → inventory; SQLite inventory: сверить артикул и quantity со складом.",
			"aiac9/d33/support_service.go → answer: проверить передачу результата инструмента и документации модели.",
		}
	default:
		return []string{
			"aiac9/d33/docs/faq.md и docs/product.md: проверить, описан ли вопрос в базе знаний.",
			"aiac9/d33/rag.go → search: проверить релевантность фрагментов и актуальность индекса.",
			"aiac9/d33/support_service.go → answer: проверить использованные инструменты и причину эскалации.",
		}
	}
}

func (s *crmStore) createTask(ctx context.Context, session, title, category, phone, reason string, sources []string) (crmTask, error) {
	title, reason = strings.TrimSpace(title), strings.TrimSpace(reason)
	if title == "" || len([]rune(title)) > 160 || len([]rune(reason)) > 4000 {
		return crmTask{}, fmt.Errorf("некорректный заголовок или описание задачи")
	}
	if category != "access" && category != "inventory" {
		category = "general"
	}
	if phone != "" {
		var err error
		phone, err = normalizePhone(phone)
		if err != nil {
			return crmTask{}, err
		}
	}
	history, err := s.conversation(ctx, session)
	if err != nil {
		return crmTask{}, err
	}
	hintsJSON, _ := json.Marshal(investigationHints(category))
	sourcesJSON, _ := json.Marshal(sources)
	if sources == nil {
		sourcesJSON = []byte(`[]`)
	}
	historyJSON, _ := json.Marshal(history)
	dedupe := session + ":" + category + ":" + phone
	if category == "general" || session == "" {
		dedupe += ":" + strings.ToLower(title)
	}
	now := s.now().Format(time.RFC3339)
	var id int64
	// Refresh the context of an active task, preserving the specialist's title and notes.
	err = s.db.QueryRowContext(ctx, `INSERT INTO tasks(title,status,category,phone,reason,session_id,code_hints,sources,transcript,dedupe_key,created_at,updated_at)
VALUES (?,'TODO',?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(dedupe_key) WHERE status!='DONE' DO UPDATE SET transcript=excluded.transcript, updated_at=excluded.updated_at
RETURNING id`, title, category, phone, reason, session, string(hintsJSON), string(sourcesJSON), string(historyJSON), dedupe, now, now).Scan(&id)
	if err != nil {
		return crmTask{}, err
	}
	return s.task(ctx, id)
}

const taskColumns = `id,title,status,category,phone,reason,session_id,code_hints,sources,transcript,created_at,updated_at`

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (crmTask, error) {
	var task crmTask
	var hints, sources, history string
	err := row.Scan(&task.ID, &task.Title, &task.Status, &task.Category, &task.Phone, &task.Reason, &task.SessionID, &hints, &sources, &history, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return task, err
	}
	if err := json.Unmarshal([]byte(hints), &task.CodeHints); err != nil {
		return task, err
	}
	if err := json.Unmarshal([]byte(sources), &task.Sources); err != nil {
		return task, err
	}
	if err := json.Unmarshal([]byte(history), &task.Messages); err != nil {
		return task, err
	}
	return task, nil
}

func (s *crmStore) task(ctx context.Context, id int64) (crmTask, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
}

func (s *crmStore) tasks(ctx context.Context) ([]crmTask, error) {
	// Board polling loads summaries; full context is requested when a card is opened.
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,status,category,phone,reason,session_id,'[]','[]','[]',created_at,updated_at FROM tasks ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []crmTask{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *crmStore) updateTask(ctx context.Context, id int64, status, title, reason string) error {
	if status != "TODO" && status != "IN PROGRESS" && status != "DONE" {
		return fmt.Errorf("неверный статус")
	}
	if strings.TrimSpace(title) == "" || len([]rune(title)) > 160 || len([]rune(reason)) > 4000 {
		return fmt.Errorf("неверный заголовок или описание")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?,title=?,reason=?,updated_at=? WHERE id=?`, status, title, reason, s.now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func validSession(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !unicode.IsDigit(r) && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
