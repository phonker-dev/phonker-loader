package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Прокси. По умолчанию («авто») берётся системный прокси Windows — его включают
// v2rayN, Happ и подобные программы в режиме «системный прокси». В режиме туннеля (TUN)
// системный прокси выключен, весь трафик и так идёт через VPN, поэтому программа
// подключается напрямую. Настройка читается перед каждой загрузкой, так что
// переключаться между режимами можно, не перезапуская программу.
//
// cfg.Proxy: "" — авто, "off" — без прокси, иначе адрес вида http://127.0.0.1:10809.

const proxyOff = "off"

// currentProxy возвращает адрес прокси (или "") и описание источника для экрана.
func currentProxy() (addr, source string) {
	switch cfg.Proxy {
	case proxyOff:
		return "", "выключен"
	case "":
		if p := systemProxy(); p != "" {
			return p, "системный"
		}
		return "", "авто: системный прокси не включён, прямое подключение"
	default:
		return cfg.Proxy, "вручную"
	}
}

func proxyArgs() []string {
	if p, _ := currentProxy(); p != "" {
		return []string{"--proxy", p}
	}
	return nil
}

// systemProxy читает HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings.
func systemProxy() string {
	path, _ := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
	var key syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, syscall.KEY_READ, &key) != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)

	var enabled uint32
	size := uint32(4)
	name, _ := syscall.UTF16PtrFromString("ProxyEnable")
	if syscall.RegQueryValueEx(key, name, nil, nil, (*byte)(unsafe.Pointer(&enabled)), &size) != nil || enabled == 0 {
		return ""
	}

	buf := make([]uint16, 1024)
	size = uint32(len(buf) * 2)
	name, _ = syscall.UTF16PtrFromString("ProxyServer")
	if syscall.RegQueryValueEx(key, name, nil, nil, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	return parseProxyServer(syscall.UTF16ToString(buf))
}

// parseProxyServer понимает "127.0.0.1:10809" и "http=127.0.0.1:10809;https=...;socks=...".
func parseProxyServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "=") {
		return normalizeProxy(s)
	}
	byScheme := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && v != "" {
			byScheme[strings.ToLower(k)] = v
		}
	}
	for _, k := range []string{"https", "http"} {
		if v := byScheme[k]; v != "" {
			return normalizeProxy(v)
		}
	}
	if v := byScheme["socks"]; v != "" {
		return normalizeProxy("socks5://" + strings.TrimPrefix(v, "socks5://"))
	}
	return ""
}

// normalizeProxy дописывает схему: "127.0.0.1:10809" -> "http://127.0.0.1:10809".
func normalizeProxy(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}

func validProxy(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Port() == "" {
		return fmt.Errorf("не понимаю адрес %q — нужен вид 127.0.0.1:10809", s)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("неизвестный тип прокси %q — поддерживаются http и socks5", u.Scheme)
}

// proxyAlive проверяет, что на адресе прокси кто-то слушает (v2rayN/Happ запущен).
func proxyAlive(addr string) bool {
	u, err := url.Parse(addr)
	if err != nil {
		return false
	}
	c, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func proxyStatusLine() string {
	addr, source := currentProxy()
	if addr == "" {
		return source
	}
	if !proxyAlive(addr) {
		return addr + " (" + source + ", " + red("не отвечает") + ")"
	}
	return addr + " (" + source + ")"
}

func proxyMenu() {
	header("ПРОКСИ")
	fmt.Println(" Авто — берётся системный прокси Windows: его включают v2rayN и Happ")
	fmt.Println(" в режиме «системный прокси». В режиме туннеля (TUN) прокси не нужен —")
	fmt.Println(" программа сама подключится напрямую, трафик и так пойдёт через VPN.")
	fmt.Println(rule())
	fmt.Println(" Сейчас: " + proxyStatusLine())
	fmt.Println()
	fmt.Println(" [1] Авто (рекомендуется)")
	fmt.Println(" [2] Без прокси")
	fmt.Println(" [3] Указать адрес вручную")
	fmt.Println(dim(" [0] Назад"))

	switch prompt("Введи номер: ") {
	case "1":
		cfg.Proxy = ""
	case "2":
		cfg.Proxy = proxyOff
	case "3":
		fmt.Println(dim(" Адрес смотри в v2rayN/Happ: «локальный порт» HTTP или mixed, например 127.0.0.1:10808."))
		in := normalizeProxy(prompt("Адрес: "))
		if in == "" {
			return
		}
		if err := validProxy(in); err != nil {
			printErr("%v", err)
			return
		}
		cfg.Proxy = in
	default:
		return
	}
	if err := saveSettings(); err != nil {
		printErr("Не удалось сохранить настройки: %v", err)
		return
	}
	printOK("Прокси: %s", proxyStatusLine())
}
