package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Музыкальные платформы: видео там нет, поэтому сразу предлагаем MP3,
// а вместо «видео» — MP4 со статичной обложкой трека (удобно выкладывать в соцсети).

var audioHosts = []string{
	"soundcloud.com", "bandcamp.com", "mixcloud.com", "audiomack.com",
	"music.youtube.com", "music.yandex.ru", "music.yandex.com", "music.yandex.by", "music.yandex.kz",
}

// isAudioSite — ссылка ведёт на музыкальную платформу.
func isAudioSite(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range audioHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// audioOnly — у источника нет видео: музыкальный сайт или все форматы без видеодорожки.
func audioOnly(link string, info *mediaInfo) bool {
	if isAudioSite(link) {
		return true
	}
	if info == nil || len(info.Formats) == 0 {
		return false
	}
	// Пустой кодек — «неизвестно» (так бывает у прямых ссылок на файлы): считаем, что видео может быть.
	for _, f := range info.Formats {
		if f.VCodec != "none" {
			return false
		}
	}
	return true
}

// coverArgs — скачать звук как есть (без перекодирования) и обложку в jpg.
func coverArgs() []string {
	return []string{"-f", "ba/b", "--write-thumbnail", "--convert-thumbnails", "jpg", "--embed-metadata"}
}

// makeCoverVideo собирает MP4 из звука и обложки, лежащей рядом (то же имя, .jpg).
// Картинка статичная, 2 кадра в секунду — кодируется за секунды и почти ничего не весит.
func makeCoverVideo(op *operation, audio, outDir string) (string, error) {
	base := strings.TrimSuffix(audio, filepath.Ext(audio))
	cover := base + ".jpg"
	out := uniquePath(filepath.Join(outDir, filepath.Base(base)+".mp4"))

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if fileExists(cover) {
		args = append(args, "-loop", "1", "-framerate", "2", "-i", cover)
	} else {
		args = append(args, "-f", "lavfi", "-i", "color=c=black:s=720x720:r=2")
	}
	args = append(args,
		"-i", audio,
		"-map", "0:v", "-map", "1:a",
		"-vf", `scale=trunc(min(1080\,iw)/2)*2:-2,format=yuv420p`,
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "stillimage",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
	)
	// С зацикленной картинкой -shortest у ffmpeg промахивается на десятки секунд
	// (кодер держит кадры в буфере), поэтому длину задаём явно — по длине звука.
	if d := mediaDuration(audio); d > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", d))
	} else {
		args = append(args, "-shortest")
	}
	args = append(args, out)
	cmd := exec.Command(tools.ffmpeg, args...)
	cmd.Env = tools.env()
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := start(cmd, op); err != nil {
		return "", err
	}
	running.Add(1)
	err := cmd.Wait()
	running.Add(-1)
	os.Remove(cover)
	if op.aborted() {
		os.Remove(out)
		return "", errors.New("отменено")
	}
	if err != nil {
		os.Remove(out)
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return "", fmt.Errorf("не удалось собрать видео с обложкой: %s", msg)
	}
	return out, nil
}

// mediaDuration — длина файла в секундах по строке «Duration: 00:03:12.45» из ffmpeg (0 — не узнали).
func mediaDuration(path string) float64 {
	cmd := exec.Command(tools.ffmpeg, "-hide_banner", "-i", path)
	cmd.Env = tools.env()
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput() // ffmpeg без выходного файла всегда завершается с ошибкой — это нормально
	m := durationRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	var h, mi int
	var s float64
	fmt.Sscanf(m[1], "%d", &h)
	fmt.Sscanf(m[2], "%d", &mi)
	fmt.Sscanf(m[3], "%g", &s)
	return float64(h*3600+mi*60) + s
}

var durationRe = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)

// uniquePath добавляет « (2)», « (3)»…, если файл уже существует.
func uniquePath(p string) string {
	if !fileExists(p) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !fileExists(c) {
			return c
		}
	}
}
