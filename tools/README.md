# Утилиты для сборки

Сюда нужно положить три файла. В репозиторий они не входят: каждый весит около 100 МБ,
а GitHub не принимает файлы больше 100 МБ. При сборке они сжимаются и встраиваются в exe.

| Файл         | Где взять |
|--------------|-----------|
| `yt-dlp.exe` | https://github.com/yt-dlp/yt-dlp/releases, файл `yt-dlp.exe` |
| `ffmpeg.exe` | https://www.gyan.dev/ffmpeg/builds/, архив `ffmpeg-release-essentials.zip`, файл `bin\ffmpeg.exe` |
| `deno.exe`   | https://github.com/denoland/deno/releases, архив `deno-x86_64-pc-windows-msvc.zip` |

ffprobe и ffplay не нужны.
