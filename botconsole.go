package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Консольный интерфейс бота: меню, панель работающего бота, запросы на доступ.
// Сам бот — в bot.go.

func botMenu() {
	for {
		header("TELEGRAM-БОТ")
		if cfg.BotToken == "" {
			fmt.Println(" Друзья присылают боту ссылку, этот ПК скачивает видео и отправляет")
			fmt.Println(" его в чат. Бесплатно и без рекламы, работает, пока открыта программа.")
			fmt.Println()
			fmt.Println(" Чтобы начать, нужен токен бота — это займёт минуту:")
			fmt.Println(" выбери [6] и следуй подсказкам.")
			fmt.Println(rule())
		}
		reqs := len(botDB.requests())
		fmt.Printf(" Друзья     : %d", len(friendsCopy()))
		if reqs > 0 {
			fmt.Print(yellow(fmt.Sprintf("   · ждут доступа: %d", reqs)))
		}
		fmt.Println()
		fmt.Println(" Автозапуск : " + autostartLabel())
		fmt.Println(" Прокси     : " + proxyStatusLine())
		fmt.Println(rule())
		fmt.Println(" [1] Запустить бота")
		if reqs > 0 {
			fmt.Println(" [2] Запросы на доступ " + yellow(fmt.Sprintf("(%d)", reqs)))
		} else {
			fmt.Println(" [2] Запросы на доступ " + dim("(нет)"))
		}
		fmt.Println(" [3] Друзья и статистика")
		fmt.Println(" [4] Журнал загрузок")
		fmt.Println(" [5] Автозапуск вместе с Windows")
		if cfg.BotToken == "" {
			fmt.Println(" [6] Токен бота " + yellow("← начни отсюда"))
		} else {
			fmt.Println(" [6] Токен бота " + dim("("+maskToken(cfg.BotToken)+")"))
		}
		fmt.Println(dim(" [0] Назад"))

		switch prompt("\nВведи номер: ") {
		case "1":
			if cfg.BotToken == "" {
				printErr("Сначала укажи токен бота: пункт [6].")
				continue
			}
			runBot()
		case "2":
			requestsMenu()
		case "3":
			friendsMenu()
		case "4":
			journalMenu()
		case "5":
			autostartMenu()
		case "6":
			tokenMenu()
		default:
			return
		}
	}
}

// requestsMenu — ответить на запросы, не запуская бота.
func requestsMenu() {
	for {
		reqs := botDB.requests()
		header("ЗАПРОСЫ НА ДОСТУП")
		if len(reqs) == 0 {
			remember(" Новых запросов нет.")
			return
		}
		for i, u := range reqs {
			when := ""
			if st, ok := botDB.user(u.ID); ok {
				when = dim("  написал " + st.LastSeen.Format("02.01 15:04"))
			}
			fmt.Printf(" [%d] %s%s\n", i+1, u.Name, when)
		}
		fmt.Println(rule())
		fmt.Println(dim(" Номер и буква: 1y — разрешить, 1n — отказать. ay — разрешить всем."))
		fmt.Println(dim(" Друг получит «доступ открыт», когда бот будет запущен."))
		in := strings.ToLower(strings.ReplaceAll(prompt("\nОтвет (Enter — назад): "), " ", ""))
		if in == "" {
			return
		}
		if in == "ay" || in == "фн" {
			for _, u := range reqs {
				_ = grantAccess(u, true)
			}
			printOK("Доступ выдан всем: %d.", len(reqs))
			continue
		}
		r := []rune(in) // по символам: на русской раскладке y/n — это «н»/«т»
		if len(r) < 2 {
			continue
		}
		n, err := strconv.Atoi(string(r[:len(r)-1]))
		answer := string(r[len(r)-1])
		if err != nil || n < 1 || n > len(reqs) {
			continue
		}
		u := reqs[n-1]
		switch answer {
		case "y", "н":
			if err := grantAccess(u, true); err != nil {
				printErr("Не удалось сохранить настройки: %v", err)
			} else {
				printOK("%s получил доступ.", u.Name)
			}
		case "n", "т":
			botDB.resolveRequest(u.ID, false, false)
			printOK("%s: в доступе отказано.", u.Name)
		}
	}
}

func maskToken(t string) string {
	id, _, _ := strings.Cut(t, ":")
	return id + ":••••" + t[max(len(t)-4, 0):]
}

// autostartStatus: "off", "on" или "other" (включён для exe из другой папки).
func autostartStatus() string {
	switch state := autostartState(); {
	case state == "":
		return "off"
	case !strings.EqualFold(state, autostartCommand()):
		return "other"
	default:
		return "on"
	}
}

func autostartLabel() string {
	switch autostartStatus() {
	case "off":
		return dim("выключен")
	case "other":
		return yellow("включён для другого расположения программы — включи заново")
	default:
		return green("включён")
	}
}

func autostartMenu() {
	fmt.Println()
	fmt.Println(" При включении Windows программа сама откроется и сразу запустит бота.")
	fmt.Println(dim(" Если переместишь exe в другую папку — включи автозапуск заново."))
	fmt.Println(" Сейчас: " + autostartLabel())
	in := prompt("\n[1] Включить  [2] Выключить  Enter — назад: ")
	var err error
	switch in {
	case "1":
		if cfg.BotToken == "" {
			printErr("Сначала укажи токен бота: пункт [6].")
			return
		}
		if err = setAutostart(true); err == nil {
			printOK("Автозапуск включён.")
		}
	case "2":
		if err = setAutostart(false); err == nil {
			printOK("Автозапуск выключен.")
		}
	default:
		return
	}
	if err != nil {
		printErr("Не удалось изменить автозапуск: %v", err)
	}
}

// checkToken проверяет токен в Telegram. Возвращает имя бота; ошибка сети — не повод отказывать.
func checkToken(token string) (name string, err error) {
	if !tokenRe.MatchString(token) {
		return "", errors.New("это не похоже на токен — он выглядит так: 123456789:AAH4b...xyz")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	me, err := newTelegram(token).getMe(ctx)
	var te *tgError
	if errors.As(err, &te) {
		return "", errors.New(explainTelegram(err))
	}
	if err != nil {
		return "", nil // нет связи — сохраним как есть
	}
	return me.Username, nil
}

func tokenMenu() {
	fmt.Println()
	fmt.Println(" Как получить токен:")
	fmt.Println("  1. Открой в Telegram @BotFather и отправь /newbot.")
	fmt.Println("  2. Придумай боту имя и адрес (должен заканчиваться на bot).")
	fmt.Println("  3. BotFather пришлёт токен вида 123456789:AAH...xyz — вставь его сюда.")
	fmt.Println(dim(" Токен — как пароль от бота: не показывай его никому и не выкладывай на GitHub."))
	fmt.Println(dim(" Он хранится только в settings.json рядом с программой."))
	in := strings.TrimSpace(prompt("\nТокен (- удалить, Enter — назад): "))
	switch in {
	case "":
		return
	case "-":
		cfg.BotToken = ""
	default:
		l := startLive("Проверяю токен...")
		name, err := checkToken(in)
		l.Stop()
		if err != nil {
			printErr("%v", err)
			return
		}
		if name == "" {
			fmt.Println(yellow(" Не удалось проверить токен: нет связи с Telegram. Сохраняю как есть."))
		} else {
			printOK("Бот @%s найден.", name)
		}
		cfg.BotToken = in
	}
	if err := saveSettings(); err != nil {
		printErr("Не удалось сохранить настройки: %v", err)
	}
}

func friendsMenu() {
	for {
		header("ДРУЗЬЯ")
		friends := friendsCopy()
		if len(friends) == 0 {
			remember(" Друзей пока нет. Пусть друг напишет боту что угодно, пока бот запущен —")
			remember(" в окне программы появится запрос, ответь на него y.")
			return
		}
		width := consoleWidth() - 1
		for i, u := range friends {
			line := fmt.Sprintf(" [%d] %s", i+1, u.Name)
			if st, ok := botDB.user(u.ID); ok {
				line += dim(fmt.Sprintf("  %d загр. · %s · был %s", st.Downloads,
					formatBytes(float64(st.Bytes)), st.LastSeen.Format("02.01 15:04")))
			}
			fmt.Println(truncate(line, width+20)) // +20 — на цветовые коды
		}
		fmt.Println(rule())
		n, err := strconv.Atoi(prompt("Номер — подробнее, Enter — назад: "))
		if err != nil || n < 1 || n > len(friends) {
			return
		}
		friendDetails(friends[n-1])
	}
}

func friendDetails(u botUser) {
	header(u.Name)
	fmt.Printf(" ID Telegram : %d\n", u.ID)
	if st, ok := botDB.user(u.ID); ok {
		lang := st.Lang
		if lang == "" {
			lang = "—"
		}
		premium := "нет"
		if st.Premium {
			premium = "да"
		}
		fmt.Printf(" Язык        : %s · Premium: %s\n", lang, premium)
		fmt.Printf(" Впервые     : %s\n", st.FirstSeen.Format("02.01.2006 15:04"))
		fmt.Printf(" Последний   : %s\n", st.LastSeen.Format("02.01.2006 15:04"))
		fmt.Printf(" Загрузок    : %d (ошибок %d) · %s\n", st.Downloads, st.Failed, formatBytes(float64(st.Bytes)))
		if sites := botDB.topSites(u.ID, 4); sites != "" {
			fmt.Printf(" Сайты       : %s\n", sites)
		}
	}
	if list := botDB.history(u.ID, 10, false); len(list) > 0 {
		fmt.Println("\n Последние загрузки:")
		printJournal(list, false)
	}
	fmt.Println(rule())
	if prompt("- — убрать доступ, Enter — назад: ") != "-" {
		return
	}
	if _, err := removeFriend(u.ID); err != nil {
		printErr("Не удалось сохранить настройки: %v", err)
		return
	}
	printOK("%s больше не может пользоваться ботом.", u.Name)
}

func journalMenu() {
	list := botDB.history(0, 30, false)
	header("ЖУРНАЛ ЗАГРУЗОК")
	if len(list) == 0 {
		fmt.Println(" Пока пусто.")
		return
	}
	printJournal(list, true)
	fmt.Println(rule())
	waitEnter("Enter — назад")
}

func printJournal(list []logEntry, withUser bool) {
	width := consoleWidth() - 1
	for _, e := range list {
		mark := green("OK ")
		what := e.Title
		if !e.OK {
			mark = red("ERR")
			what = e.Error
		}
		if what == "" {
			what = e.URL
		}
		if e.Audio {
			what = "[mp3] " + what
		}
		who := ""
		if withUser {
			if st, ok := botDB.user(e.UserID); ok {
				who = truncate(st.Name, 18) + " · "
			}
		}
		size := ""
		if e.Size > 0 {
			size = " · " + formatBytes(float64(e.Size))
		}
		line := fmt.Sprintf(" %s %s %s%s%s", e.Time.Format("02.01 15:04"), mark, who, what, size)
		fmt.Println(truncate(line, width+10))
	}
}

// ---------- работающий бот в консоли ----------

// runBot запускает бота; R в панели перезапускает его (настройки перечитываются заново).
func runBot() {
	for runBotOnce() {
		loadSettings()
	}
}

func runBotOnce() (restart bool) {
	header("TELEGRAM-БОТ")
	l := startLive("Подключаюсь к Telegram...")
	b, err := startBot(true)
	l.Stop()
	if err != nil {
		printErr("%v", err)
		return false
	}
	proxy := proxyStatusLine()
	b.onChange = func() { b.render(proxy) }

	botActive.Store(true)
	cancelled.Store(false)
	setTitle("бот @" + b.name)
	defer func() {
		botActive.Store(false)
		cancelled.Store(false)
		setTitle("")
	}()

	b.render(proxy)
	restart = b.console()
	fmt.Println()
	if restart {
		b.stop(false)
		flushInput()
		return true
	}
	l = startLive("Останавливаю бота, ставлю статус «не в сети»...")
	b.stop(true)
	l.Stop()
	flushInput()
	printOK("Бот остановлен. За сессию отправлено: %d (%s).",
		b.sessionOK.Load(), formatBytes(float64(b.sessionBytes.Load())))
	return false
}

const botEventsShown = 12

// render перерисовывает панель бота целиком: заголовок, сводку и последние события.
// Без поддержки ANSI (старые консоли) события просто печатаются по одному (см. note).
func (b *bot) render(proxy string) {
	if !vt || !isConsole {
		return
	}
	b.viewMu.Lock()
	defer b.viewMu.Unlock()
	width := consoleWidth() - 1

	clearScreen()
	fmt.Println(rule())
	fmt.Println(center("БОТ @"+b.name+" РАБОТАЕТ", ruleWidth()))
	fmt.Println(rule())
	fmt.Println(" Ссылка   : t.me/" + b.name)
	fmt.Println(" Прокси   : " + proxy)
	fmt.Printf(" Друзья   : %d · отправлено: %d (%s) · в работе: %d\n", len(friendsCopy()),
		b.sessionOK.Load(), formatBytes(float64(b.sessionBytes.Load())), b.active.Load())
	fmt.Println(rule())
	events := b.events
	if len(events) > botEventsShown {
		events = events[len(events)-botEventsShown:]
	}
	if len(events) == 0 {
		fmt.Println(dim(" Пока тихо. Когда друзья пришлют ссылку — увидишь здесь."))
	}
	for _, ev := range events {
		fmt.Println(truncate(" "+dim(ev.time.Format("15:04"))+"  "+ev.text+ev.status, width+20))
	}
	fmt.Println(rule())
	fmt.Println(dim(" Enter — остановить · R — перезапустить · неотвеченные запросы сохранятся"))
	if b.question != "" {
		fmt.Print("\n" + b.question)
	}
}

func (b *bot) ask(question string) {
	b.viewMu.Lock()
	b.question = question
	b.viewMu.Unlock()
	if (!vt || !isConsole) && question != "" {
		fmt.Print("\n" + question)
	}
	b.changed()
}

// console обрабатывает ввод владельца: ответы на запросы доступа, остановку (Enter)
// и перезапуск (R). Возвращает true, если нужен перезапуск.
func (b *bot) console() (restart bool) {
	startInput()
	queue := botDB.requests() // запросы, пришедшие, пока бот был выключен или до перезапуска
	askNext := func() {
		if len(queue) == 0 {
			b.ask("")
			return
		}
		more := ""
		if len(queue) > 1 {
			more = dim(fmt.Sprintf(" (ещё %d в очереди)", len(queue)-1))
		}
		b.ask(fmt.Sprintf("%s %s хочет пользоваться ботом%s. Разрешить? [y/n]: ",
			yellow("[?]"), bold(queue[0].Name), more))
	}
	askNext()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case u := <-b.requests:
			queue = append(queue, u)
			askNext()
		case line, ok := <-inputLines:
			if !ok {
				return false
			}
			switch strings.ToLower(line) {
			case "", "0", "q":
				return false // неотвеченные запросы сохранятся до следующего запуска
			case "r", "к", "restart":
				return true
			}
			if len(queue) == 0 {
				b.changed()
				continue
			}
			u := queue[0]
			switch strings.ToLower(line) {
			case "y", "yes", "д", "да", "н":
				b.allow(u)
			case "n", "no", "нет", "т":
				b.deny(u)
			default:
				askNext()
				continue
			}
			queue = queue[1:]
			askNext()
		case <-tick.C:
			if cancelled.Load() {
				return false
			}
		}
	}
}
