# Cloudflare Tunnel service manager

`tunnelctl` controla el servicio de `cloudflared` que ya tienes instalado en macOS. Usa la biblioteca estándar de Go y ejecuta `launchctl` por dentro. Puedes consultar el estado sin `sudo`; los cambios de estado solicitan la contraseña de administrador cuando hace falta.

El servicio predeterminado es `system/com.cloudflare.cloudflared`, con su archivo en `/Library/LaunchDaemons/com.cloudflare.cloudflared.plist`. Corresponde al LaunchDaemon instalado con `sudo cloudflared service install`, aunque hayas instalado el binario de `cloudflared` mediante Homebrew. Cloudflare documenta este tipo de instalación en su [guía para macOS](https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/as-a-service/macos/).

## Compilar y usar

Requiere macOS y Go 1.26 o posterior.

```sh
go build -o tunnelctl .

./tunnelctl status
./tunnelctl list
./tunnelctl stop
./tunnelctl start
./tunnelctl restart
./tunnelctl --help
./tunnelctl -h
./tunnelctl help
```

Ejemplo de estado:

```text
com.cloudflare.cloudflared: en ejecución, PID 34996.
```

| Comando | Efecto |
| --- | --- |
| `start` | Habilita el servicio, carga el plist si hace falta y lo inicia. Si ya está corriendo, mantiene el proceso. |
| `stop` | Deshabilita y descarga el servicio. Permanece apagado incluso después de reiniciar el Mac, hasta ejecutar `start` o `restart`. |
| `restart` | Habilita y reinicia el proceso. Si estaba descargado, lo carga e inicia. |
| `status` | Muestra el estado del proceso local y su PID si está corriendo. |
| `list` | Lista los labels instalados en `/Library/LaunchDaemons`, incluso si están detenidos. No necesita `sudo`. |
| `help`, `-h`, `--help` | Explica comandos, parámetros y ejemplos. También puedes usar `tunnelctl <comando> -h`. |

`stop` y `restart` interrumpen las conexiones que pasan por este conector. Si hay otros conectores del mismo túnel, estos comandos solo afectan al instalado en este Mac.

`status` consulta el proceso local. No comprueba la conexión con Cloudflare ni la disponibilidad del origen. El estado detenido es una consulta válida y devuelve código 0; los errores de permisos, ejecución o argumentos devuelven código 1.

Después de `start` o `restart`, la utilidad espera hasta cinco segundos a que el proceso aparezca en ejecución. Si no arranca, devuelve un error. Esto verifica el proceso, sin esperar a que el túnel se conecte con Cloudflare.

## Otro nombre de servicio

Usa `list` para descubrir los nombres disponibles. Después puedes pasar uno mediante `--label`:

```sh
./tunnelctl list
./tunnelctl status --label com.cloudflare.cloudflared.otro
./tunnelctl start --label com.cloudflare.cloudflared.otro
```

La utilidad busca `/Library/LaunchDaemons/<label>.plist`. Está limitada al dominio `system`; no incluye agentes de usuario ni los servicios de `/System/Library/LaunchDaemons`. Un servicio de Homebrew instalado en `/Library/LaunchDaemons` también puede aparecer en la lista.

`list` lee el campo `Label` de cada plist y muestra solo los nombres válidos cuyo archivo se llama `<label>.plist`, que es la convención que usa esta utilidad. Puede mostrar servicios de otras aplicaciones, además de `cloudflared`. Si algún plist no se puede leer, devuelve un error con la ruta del archivo.

Puedes copiar el binario a un directorio incluido en tu `PATH` para usar `tunnelctl` sin el prefijo `./`.

## Pruebas

```sh
go test ./...
go vet ./...
```

Las pruebas simulan las llamadas a `launchctl`, sin modificar servicios reales. La lectura de estado interpreta únicamente los campos principales de `launchctl print`; Apple advierte en `man launchctl` que ese formato puede cambiar. Si faltan los campos esperados, la utilidad devuelve un error.
