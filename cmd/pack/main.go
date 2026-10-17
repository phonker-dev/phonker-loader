// pack сжимает утилиты из папки tools/ в assets/*.gz, которые встраиваются в exe.
// Запуск из корня проекта: go run ./cmd/pack
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ffprobe и ffplay не нужны: yt-dlp обходится одним ffmpeg.
var tools = []string{"yt-dlp.exe", "ffmpeg.exe", "deno.exe"}

func main() {
	if err := os.MkdirAll("assets", 0o755); err != nil {
		fail(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(tools))
	for _, name := range tools {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := pack(filepath.Join("tools", name), filepath.Join("assets", name+".gz")); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		fail(err)
	}

	out, err := exec.Command(filepath.Join("tools", "yt-dlp.exe"), "--version").Output()
	if err != nil {
		fail(fmt.Errorf("yt-dlp --version: %w", err))
	}
	version := strings.TrimSpace(string(out))
	if err := os.WriteFile(filepath.Join("assets", "yt-dlp.version"), []byte(version), 0o644); err != nil {
		fail(err)
	}
	fmt.Println("yt-dlp", version)
}

func pack(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("не найден %s (откуда скачать — см. tools/README.md)", src)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	if _, err = io.Copy(zw, in); err == nil {
		err = zw.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	di, _ := os.Stat(dst)
	fmt.Printf("%-18s %4d МБ -> %3d МБ\n", src, si.Size()>>20, di.Size()>>20)
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}
