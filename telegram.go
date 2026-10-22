package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Минимальный клиент Telegram Bot API (https://core.telegram.org/bots/api).
// Запросы идут через тот же прокси, что и загрузки.

type tgClient struct {
	base string
	http *http.Client
}

type tgUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	IsPremium    bool   `json:"is_premium"`
}

func (u *tgUser) displayName() string {
	name := cleanName(u.FirstName + " " + u.LastName)
	if name == "" {
		name = "id " + strconv.FormatInt(u.ID, 10)
	}
	if u.Username != "" {
		name += " (@" + u.Username + ")"
	}
	return name
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

// tgButton — кнопка под сообщением; нажатие приходит боту как callback_query с Data.
type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

func keyboard(buttons ...tgButton) map[string]any {
	return map[string]any{"inline_keyboard": [][]tgButton{buttons}}
}

type tgError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *tgError) Error() string {
	return fmt.Sprintf("Telegram: %s (код %d)", e.Description, e.Code)
}

func newTelegram(token string) *tgClient {
	root := "https://api.telegram.org"
	if r := os.Getenv("PHONKER_TG_API"); r != "" { // для тестов с локальным сервером
		root = r
	}
	return &tgClient{base: root + "/bot" + token + "/", http: proxiedClient()}
}

func (c *tgClient) call(ctx context.Context, method string, params any, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *tgClient) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("Telegram вернул непонятный ответ (HTTP %d)", resp.StatusCode)
	}
	if !r.OK {
		return &tgError{Code: r.ErrorCode, Description: r.Description, RetryAfter: r.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (c *tgClient) getMe(ctx context.Context) (*tgUser, error) {
	var me tgUser
	err := c.call(ctx, "getMe", map[string]any{}, &me)
	return &me, err
}

func (c *tgClient) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var ups []tgUpdate
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// sendMessage отправляет текст с HTML-разметкой. replyTo — ответить на сообщение (0 — нет).
func (c *tgClient) sendMessage(ctx context.Context, chatID int64, html string, replyTo int64, markup any) (int64, error) {
	p := map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if replyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	if markup != nil {
		p["reply_markup"] = markup
	}
	var m tgMessage
	err := c.call(ctx, "sendMessage", p, &m)
	return m.MessageID, err
}

// sendSilent отправляет сообщение без звука уведомления (для закреплённого статуса).
func (c *tgClient) sendSilent(ctx context.Context, chatID int64, html string) (int64, error) {
	var m tgMessage
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML", "disable_notification": true,
	}, &m)
	return m.MessageID, err
}

func (c *tgClient) pinMessage(ctx context.Context, chatID, msgID int64) error {
	return c.call(ctx, "pinChatMessage", map[string]any{
		"chat_id": chatID, "message_id": msgID, "disable_notification": true,
	}, nil)
}

func (c *tgClient) editMessage(ctx context.Context, chatID, msgID int64, html string) error {
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": msgID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	}, nil)
}

func (c *tgClient) answerCallback(ctx context.Context, id, text string) {
	_ = c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// setProfile задаёт описание бота (видно до /start), короткое описание (в профиле) и команды.
func (c *tgClient) setProfile(ctx context.Context, description, short string) error {
	if err := c.call(ctx, "setMyDescription", map[string]any{"description": description}, nil); err != nil {
		return err
	}
	return c.call(ctx, "setMyShortDescription", map[string]any{"short_description": short}, nil)
}

func (c *tgClient) deleteMessage(ctx context.Context, chatID, msgID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

func (c *tgClient) sendChatAction(ctx context.Context, chatID int64, action string) {
	_ = c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// deleteCommands убирает меню команд «/» — их заменяют кнопки внизу чата.
func (c *tgClient) deleteCommands(ctx context.Context) {
	_ = c.call(ctx, "deleteMyCommands", map[string]any{}, nil)
}

// sendFile загружает файл методом sendVideo/sendAudio/sendDocument.
// Файл передаётся потоком, целиком в память не читается.
func (c *tgClient) sendFile(ctx context.Context, method, field string, chatID int64, path string, extra map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
				return err
			}
			for k, v := range extra {
				if v == "" {
					continue // неизвестные поля (ширина, длительность) просто не передаём
				}
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile(field, filepath.Base(path))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(req, nil)
}

// explainTelegram переводит ошибки Telegram для консоли владельца.
func explainTelegram(err error) string {
	var te *tgError
	if errors.As(err, &te) {
		switch te.Code {
		case 401, 404:
			return "Telegram не принял токен. Проверь его: Telegram-бот → Указать токен."
		case 409:
			return "Этот бот уже запущен в другом месте (ещё одна копия программы?). Закрой её."
		case 413:
			return "файл слишком большой для Telegram"
		}
		return te.Error()
	}
	return "нет связи с Telegram: " + err.Error() + "\n       " + networkHint()
}
