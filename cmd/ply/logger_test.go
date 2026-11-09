package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerMultiWriter(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	logPath := filepath.Join(logDir, "ply.log")

	var stderr bytes.Buffer
	logger, cleanup := setupLoggerWithWriter(logDir, logPath, 1024*1024, 3, &stderr)
	defer cleanup()

	logger.Info("hello dual writer", "key", "val123")

	// Verify stderr received the log
	stderrOut := stderr.String()
	if !strings.Contains(stderrOut, "hello dual writer") || !strings.Contains(stderrOut, "key=val123") {
		t.Fatalf("stderr lacked expected log line: %q", stderrOut)
	}

	// Verify file received the log
	fileBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)
	if !strings.Contains(fileOut, "hello dual writer") || !strings.Contains(fileOut, "key=val123") {
		t.Fatalf("log file lacked expected log line: %q", fileOut)
	}
}

func TestLoggerRotation(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	logPath := filepath.Join(logDir, "ply.log")

	var stderr bytes.Buffer
	// Small maxSize of 80 bytes to trigger rotation quickly
	logger, cleanup := setupLoggerWithWriter(logDir, logPath, 80, 3, &stderr)
	defer cleanup()

	// Line 1: fits in first file
	logger.Info("msg 1: first message in ply.log that is relatively long")
	// Line 2: exceeds 80 bytes, rotates ply.log -> ply.1.log
	logger.Info("msg 2: second message in ply.log that triggers first rotation")

	// Positive control: ply.1.log must exist after rotation
	if _, err := os.Stat(filepath.Join(logDir, "ply.1.log")); err != nil {
		t.Fatalf("expected ply.1.log to exist: %v", err)
	}

	// Line 3: triggers second rotation -> ply.2.log
	logger.Info("msg 3: third message in ply.log that triggers second rotation")
	// Line 4: triggers third rotation -> ply.3.log
	logger.Info("msg 4: fourth message in ply.log that triggers third rotation")
	// Line 5: triggers fourth rotation -> maxBackups is 3, ply.4.log should not be created
	logger.Info("msg 5: fifth message in ply.log that drops oldest backup")

	// Positive controls: ply.1.log, ply.2.log, ply.3.log all exist
	for _, name := range []string{"ply.log", "ply.1.log", "ply.2.log", "ply.3.log"} {
		p := filepath.Join(logDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}

	// Absence assertion with positive control above: ply.4.log must NOT exist
	p4 := filepath.Join(logDir, "ply.4.log")
	if _, err := os.Stat(p4); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ply.4.log to not exist, got err: %v", err)
	}
}

func TestLoggerFallbackOnUnwritableDir(t *testing.T) {
	tmp := t.TempDir()
	// Create a regular file where the directory should be
	fileAsDir := filepath.Join(tmp, "file_as_dir")
	if err := os.WriteFile(fileAsDir, []byte("blocking file"), 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(fileAsDir, "logs")
	logPath := filepath.Join(logDir, "ply.log")

	var stderr bytes.Buffer
	logger, cleanup := setupLoggerWithWriter(logDir, logPath, 1024*1024, 3, &stderr)
	defer cleanup()

	// Should have logged a warning to stderr
	if !strings.Contains(stderr.String(), "cannot create log directory") {
		t.Fatalf("expected warning about creating log dir in stderr: %q", stderr.String())
	}

	// Positive control: writes to logger still appear in stderr without crashing
	logger.Info("fallback test message")
	if !strings.Contains(stderr.String(), "fallback test message") {
		t.Fatalf("expected fallback message in stderr: %q", stderr.String())
	}
}

func TestLoggerFallbackOnUnwritableFile(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Make a directory with the same name as the log file, so OpenFile fails with EISDIR
	logPath := filepath.Join(logDir, "ply.log")
	if err := os.Mkdir(logPath, 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	logger, cleanup := setupLoggerWithWriter(logDir, logPath, 1024*1024, 3, &stderr)
	defer cleanup()

	// Should have logged a warning to stderr
	if !strings.Contains(stderr.String(), "cannot open log file") {
		t.Fatalf("expected warning about opening log file in stderr: %q", stderr.String())
	}

	// Positive control: writes to logger still appear in stderr without crashing
	logger.Info("fallback file message")
	if !strings.Contains(stderr.String(), "fallback file message") {
		t.Fatalf("expected fallback message in stderr: %q", stderr.String())
	}
}
