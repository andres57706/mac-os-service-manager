package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLabel     = "com.cloudflare.cloudflared"
	launchDaemonsDir = "/Library/LaunchDaemons"
)

var validLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type service struct {
	label string
	plist string
	run   func(...string) ([]byte, error)
}

type serviceStatus struct {
	loaded bool
	state  string
	pid    int
}

func main() {
	if err := run(os.Args[1:], os.Stdout, launchctl); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, execute func(...string) ([]byte, error)) error {
	usage := func() {
		fmt.Fprintln(out, `Uso: tunnelctl <comando> [opciones]

Comandos:
  start     Habilita e inicia el servicio.
  stop      Detiene el servicio e impide su arranque, incluso tras reiniciar el Mac.
  restart   Habilita y reinicia el servicio, o lo inicia si estaba detenido.
  status    Muestra el estado local y el PID, sin pedir sudo.
  list      Lista los labels disponibles en /Library/LaunchDaemons, sin pedir sudo.
  help      Muestra esta ayuda.

Opciones después del comando:
  --label nombre   Selecciona el servicio. Predeterminado: com.cloudflare.cloudflared.
  -h, --help       Muestra esta ayuda. También funcionan sin un comando.

Ejemplos:
  tunnelctl list
  tunnelctl status
  tunnelctl stop
  tunnelctl start
  tunnelctl restart --label com.cloudflare.cloudflared.otro
  tunnelctl status -h

El parámetro --label se admite con start, stop, restart y status.
Los cambios de estado piden sudo cuando hacen falta permisos de administrador.
Stop y restart interrumpen las conexiones de este conector.
Status consulta el proceso local, sin comprobar la conexión con Cloudflare.`)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage()
		return nil
	}

	command := args[0]
	switch command {
	case "start", "stop", "restart", "status", "list":
	default:
		return fmt.Errorf("comando desconocido %q; usa tunnelctl --help", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = usage
	label := defaultLabel
	if command != "list" {
		flags.StringVar(&label, "label", defaultLabel, "Nombre del LaunchDaemon")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("argumentos inesperados: %s", strings.Join(flags.Args(), " "))
	}
	if command == "list" {
		if runtime.GOOS != "darwin" {
			return errors.New("tunnelctl requiere macOS")
		}
		labels, err := installedLabels(launchDaemonsDir, plistLabel)
		if err != nil {
			return err
		}
		if len(labels) == 0 {
			_, err = fmt.Fprintln(out, "No se encontraron labels disponibles en", launchDaemonsDir)
			return err
		}
		_, err = fmt.Fprintln(out, strings.Join(labels, "\n"))
		return err
	}
	if !validLabel.MatchString(label) {
		return fmt.Errorf("label inválido %q; usa letras, números, puntos, guiones o guiones bajos", label)
	}
	s := service{
		label: label,
		plist: filepath.Join(launchDaemonsDir, label+".plist"),
		run:   execute,
	}

	if command == "stop" {
		if err := s.stop(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "%s: detenido. Permanecerá apagado hasta ejecutar start o restart.\n", s.label)
		return err
	}
	var status serviceStatus
	var err error
	if command == "start" || command == "restart" {
		if err := s.start(command == "restart"); err != nil {
			return err
		}
		status, err = s.waitRunning(5 * time.Second)
	} else {
		status, err = s.status()
	}
	if err != nil {
		return err
	}
	if !status.loaded {
		_, err = fmt.Fprintf(out, "%s: detenido, no cargado en launchd.\n", s.label)
	} else if status.state == "running" {
		_, err = fmt.Fprintf(out, "%s: en ejecución, PID %d.\n", s.label, status.pid)
	} else {
		_, err = fmt.Fprintf(out, "%s: cargado, estado %s.\n", s.label, status.state)
	}
	return err
}

func installedLabels(dir string, readLabel func(string) (string, error)) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no se pueden listar los servicios de %s: %w", dir, err)
	}
	var labels []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}
		plist := filepath.Join(dir, entry.Name())
		label, err := readLabel(plist)
		if err != nil {
			return nil, fmt.Errorf("no se puede leer el Label de %s: %w", plist, err)
		}
		if validLabel.MatchString(label) && entry.Name() == label+".plist" {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	return labels, nil
}

func plistLabel(plist string) (string, error) {
	output, err := exec.Command("/usr/bin/plutil", "-extract", "Label", "raw", "-expect", "string", "-o", "-", "-n", "--", plist).Output()
	return string(output), err
}

func (s service) status() (serviceStatus, error) {
	output, err := s.run("print", "system/"+s.label)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 113 {
			return serviceStatus{}, nil
		}
		return serviceStatus{}, err
	}

	status := serviceStatus{loaded: true}
	// shortcut: launchctl print has no stable format; update this parser if macOS changes it.
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(line, "\tstate = "); ok {
			status.state = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "\tpid = "); ok {
			status.pid, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || status.pid <= 0 {
				return serviceStatus{}, errors.New("launchctl devolvió un PID inválido")
			}
		}
	}
	if status.state == "" || (status.state == "running" && status.pid == 0) {
		return serviceStatus{}, errors.New("no se pudo interpretar el estado de launchctl; el formato de macOS puede haber cambiado")
	}
	return status, nil
}

func (s service) start(restart bool) error {
	status, err := s.status()
	if err != nil {
		return err
	}
	if !status.loaded {
		info, err := os.Stat(s.plist)
		if err != nil {
			return fmt.Errorf("no se puede abrir el servicio instalado en %s: %w", s.plist, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("el plist debe ser un archivo: %s", s.plist)
		}
	}
	target := "system/" + s.label
	if _, err := s.run("enable", target); err != nil {
		return err
	}
	if !status.loaded {
		if _, err := s.run("bootstrap", "system", s.plist); err != nil {
			return err
		}
	}
	args := []string{"kickstart"}
	if restart && status.loaded {
		args = append(args, "-k")
	}
	_, err = s.run(append(args, target)...)
	return err
}

func (s service) waitRunning(timeout time.Duration) (serviceStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := s.status()
		if err != nil || status.state == "running" {
			return status, err
		}
		if !time.Now().Before(deadline) {
			state := status.state
			if !status.loaded {
				state = "no cargado"
			}
			return status, fmt.Errorf("el servicio no quedó en ejecución tras %s; estado: %s; revisa los logs de cloudflared", timeout, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s service) stop() error {
	status, err := s.status()
	if err != nil {
		return err
	}
	target := "system/" + s.label
	if _, err := s.run("disable", target); err != nil {
		return err
	}
	if status.loaded {
		_, err = s.run("bootout", target)
	}
	return err
}

func launchctl(args ...string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("tunnelctl requiere macOS")
	}
	program := "/bin/launchctl"
	if args[0] != "print" && os.Geteuid() != 0 {
		args = append([]string{program}, args...)
		program = "/usr/bin/sudo"
	}
	cmd := exec.Command(program, args...)
	cmd.Stdin = os.Stdin
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", program, strings.Join(args, " "), err, strings.TrimSpace(diagnostic.String()))
	}
	return output, nil
}
