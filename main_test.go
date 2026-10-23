package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const runningOutput = `system/com.cloudflare.cloudflared = {
	state = running
	arguments = {
		--token
		secret-test-token
	}
	pid = 34996
	resource coalition = {
		state = active
	}
}
`

func exitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected subprocess error")
	}
	return fmt.Errorf("launchctl failed: %w", err)
}

func TestStatus(t *testing.T) {
	missing := exitError(t, 113)
	permission := exitError(t, 1)
	for _, tt := range []struct {
		name   string
		output string
		err    error
		want   serviceStatus
		bad    bool
	}{
		{name: "running with nested state", output: runningOutput, want: serviceStatus{loaded: true, state: "running", pid: 34996}},
		{name: "loaded but stopped", output: "service = {\n\tstate = not running\n}\n", want: serviceStatus{loaded: true, state: "not running"}},
		{name: "unloaded", err: missing},
		{name: "permission denied is not stopped", err: permission, bad: true},
		{name: "execution failed", err: errors.New("cannot execute"), bad: true},
		{name: "unknown format", output: "unknown output", bad: true},
		{name: "running without PID", output: "\tstate = running\n", bad: true},
		{name: "invalid PID", output: "\tstate = running\n\tpid = invalid\n", bad: true},
		{name: "zero PID", output: "\tstate = running\n\tpid = 0\n", bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := service{label: defaultLabel, run: func(args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args, []string{"print", "system/" + defaultLabel}) {
					t.Fatalf("unexpected arguments: %q", args)
				}
				return []byte(tt.output), tt.err
			}}
			got, err := s.status()
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("status = %+v, %v; want %+v, error=%v", got, err, tt.want, tt.bad)
			}
		})
	}
}

func TestTransitions(t *testing.T) {
	missing := exitError(t, 113)
	for _, tt := range []struct {
		name   string
		action string
		loaded bool
		want   [][]string
	}{
		{name: "start loaded", action: "start", loaded: true, want: [][]string{{"enable", "system/" + defaultLabel}, {"kickstart", "system/" + defaultLabel}}},
		{name: "start unloaded", action: "start", want: [][]string{{"enable", "system/" + defaultLabel}, {"bootstrap", "system", "PLIST"}, {"kickstart", "system/" + defaultLabel}}},
		{name: "restart loaded", action: "restart", loaded: true, want: [][]string{{"enable", "system/" + defaultLabel}, {"kickstart", "-k", "system/" + defaultLabel}}},
		{name: "restart unloaded", action: "restart", want: [][]string{{"enable", "system/" + defaultLabel}, {"bootstrap", "system", "PLIST"}, {"kickstart", "system/" + defaultLabel}}},
		{name: "stop loaded", action: "stop", loaded: true, want: [][]string{{"disable", "system/" + defaultLabel}, {"bootout", "system/" + defaultLabel}}},
		{name: "stop unloaded", action: "stop", want: [][]string{{"disable", "system/" + defaultLabel}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plist := t.TempDir() + "/service.plist"
			if err := os.WriteFile(plist, nil, 0600); err != nil {
				t.Fatal(err)
			}
			var calls [][]string
			s := service{label: defaultLabel, plist: plist, run: func(args ...string) ([]byte, error) {
				if args[0] == "print" {
					if tt.loaded {
						return []byte(runningOutput), nil
					}
					return nil, missing
				}
				calls = append(calls, append([]string(nil), args...))
				return nil, nil
			}}
			var err error
			if tt.action == "stop" {
				err = s.stop()
			} else {
				err = s.start(tt.action == "restart")
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range tt.want {
				for i, arg := range call {
					if arg == "PLIST" {
						call[i] = plist
					}
				}
			}
			if !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("calls = %q; want %q", calls, tt.want)
			}
		})
	}
}

func TestTransitionStopsOnError(t *testing.T) {
	missing := exitError(t, 113)
	for _, action := range []string{"start", "restart", "stop"} {
		for _, failAt := range []string{"print", "enable", "bootstrap", "kickstart", "disable", "bootout"} {
			if action == "stop" && (failAt == "enable" || failAt == "bootstrap" || failAt == "kickstart") {
				continue
			}
			if action != "stop" && (failAt == "disable" || failAt == "bootout") {
				continue
			}
			t.Run(action+"/"+failAt, func(t *testing.T) {
				plist := t.TempDir() + "/service.plist"
				if err := os.WriteFile(plist, nil, 0600); err != nil {
					t.Fatal(err)
				}
				failure := errors.New("test failure")
				failed := false
				s := service{label: defaultLabel, plist: plist, run: func(args ...string) ([]byte, error) {
					if failed {
						t.Fatalf("executed %q after failure", args)
					}
					if args[0] == failAt {
						failed = true
						return nil, failure
					}
					if args[0] == "print" {
						if action == "stop" {
							return []byte(runningOutput), nil
						}
						return nil, missing
					}
					return nil, nil
				}}
				var err error
				if action == "stop" {
					err = s.stop()
				} else {
					err = s.start(action == "restart")
				}
				if !errors.Is(err, failure) {
					t.Fatalf("error = %v; want %v", err, failure)
				}
			})
		}
	}
}

func TestStartRequiresInstalledPlist(t *testing.T) {
	missing := exitError(t, 113)
	for _, plist := range []string{t.TempDir() + "/missing.plist", t.TempDir()} {
		s := service{label: defaultLabel, plist: plist, run: func(args ...string) ([]byte, error) {
			if args[0] != "print" {
				t.Fatalf("unexpected mutation: %q", args)
			}
			return nil, missing
		}}
		if err := s.start(false); err == nil {
			t.Fatal("expected invalid plist error")
		}
	}
}

func TestWaitRunning(t *testing.T) {
	calls := 0
	s := service{label: defaultLabel, run: func(...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("\tstate = spawning\n"), nil
		}
		return []byte(runningOutput), nil
	}}
	status, err := s.waitRunning(time.Second)
	if err != nil || status.pid != 34996 || calls != 2 {
		t.Fatalf("status = %+v, error = %v, calls = %d", status, err, calls)
	}

	for _, output := range []string{"\tstate = not running\n", "unexpected format"} {
		s.run = func(...string) ([]byte, error) { return []byte(output), nil }
		if _, err := s.waitRunning(0); err == nil {
			t.Fatal("expected startup error")
		}
	}
	missing := exitError(t, 113)
	s.run = func(...string) ([]byte, error) { return nil, missing }
	if _, err := s.waitRunning(0); err == nil {
		t.Fatal("expected startup error for unloaded service")
	}
}

func TestCLI(t *testing.T) {
	for _, tt := range []struct {
		args []string
		bad  bool
	}{
		{},
		{args: []string{"help"}},
		{args: []string{"-h"}},
		{args: []string{"--help"}},
		{args: []string{"status", "--help"}},
		{args: []string{"list", "-h"}},
		{args: []string{"list", "--label", defaultLabel}, bad: true},
		{args: []string{"list", "extra"}, bad: true},
		{args: []string{"unknown"}, bad: true},
		{args: []string{"start", "--unknown"}, bad: true},
		{args: []string{"stop", "extra"}, bad: true},
		{args: []string{"status", "--label", ""}, bad: true},
		{args: []string{"start", "--label", "../other"}, bad: true},
		{args: []string{"stop", "--label", "service\nother"}, bad: true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			err := run(tt.args, io.Discard, func(args ...string) ([]byte, error) {
				t.Fatalf("unexpected call: %q", args)
				return nil, nil
			})
			if (err != nil) != tt.bad {
				t.Fatalf("error = %v; want error=%v", err, tt.bad)
			}
		})
	}

	var out bytes.Buffer
	err := run([]string{"status", "--label", "com.cloudflare.cloudflared.other"}, &out, func(args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"print", "system/com.cloudflare.cloudflared.other"}) {
			t.Fatalf("unexpected call: %q", args)
		}
		return []byte(runningOutput), nil
	})
	if err != nil || out.String() != "com.cloudflare.cloudflared.other: en ejecución, PID 34996.\n" {
		t.Fatalf("output = %q, error = %v", out.String(), err)
	}
	if strings.Contains(out.String(), "secret-test-token") {
		t.Fatal("status exposed token")
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}, {"start", "-h"}, {"stop", "--help"}, {"restart", "-h"}, {"status", "--help"}, {"list", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := run(args, &out, func(args ...string) ([]byte, error) {
				t.Fatalf("help called launchctl: %q", args)
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"start", "stop", "restart", "status", "list", "help", "--label", "-h, --help", "Ejemplos:", defaultLabel} {
				if !strings.Contains(out.String(), text) {
					t.Errorf("help does not explain %q", text)
				}
			}
		})
	}
}

func TestInstalledLabels(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"com.example.plist":     "com.example",
		"com.example.sub.plist": "com.example.sub",
		"other.plist":           "different.label",
		"invalid.plist":         "invalid/label",
		"notes.txt":             "ignored",
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "directory.plist"), 0700); err != nil {
		t.Fatal(err)
	}
	labels, err := installedLabels(dir, func(plist string) (string, error) {
		name := filepath.Base(plist)
		if name == "notes.txt" || name == "directory.plist" {
			t.Fatalf("read non-plist file %s", plist)
		}
		return files[name], nil
	})
	if err != nil || !reflect.DeepEqual(labels, []string{"com.example", "com.example.sub"}) {
		t.Fatalf("labels = %q, error = %v", labels, err)
	}

	failure := errors.New("unreadable plist")
	if _, err := installedLabels(dir, func(string) (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatalf("error = %v; want %v", err, failure)
	}
	if _, err := installedLabels(filepath.Join(dir, "missing"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v; want missing directory error", err)
	}
	labels, err = installedLabels(t.TempDir(), func(string) (string, error) {
		t.Fatal("read a plist in an empty directory")
		return "", nil
	})
	if err != nil || len(labels) != 0 {
		t.Fatalf("empty directory: labels = %q, error = %v", labels, err)
	}
}

func TestCLIActions(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			var out bytes.Buffer
			err := run([]string{action}, &out, func(args ...string) ([]byte, error) {
				if args[0] == "print" {
					return []byte(runningOutput), nil
				}
				return nil, nil
			})
			if err != nil || !strings.Contains(out.String(), defaultLabel) {
				t.Fatalf("output = %q, error = %v", out.String(), err)
			}
			if strings.Contains(out.String(), "secret-test-token") {
				t.Fatal("action exposed token")
			}
		})
	}
}
