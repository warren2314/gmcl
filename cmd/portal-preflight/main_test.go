package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunRejectsUnsafeTimeoutBeforeDatabaseAccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"-timeout=0"}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("exit code = %d", exitCode)
	}
	var result preflightResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.Status != "failed" ||
		len(result.Issues) != 1 ||
		!strings.Contains(result.Issues[0], "timeout") {
		t.Fatalf("result = %#v", result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestRunReturnsUsageExitCodeForInvalidFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run([]string{"-unknown"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestEmitResultSortsOutputAndReportsEncodingFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := emitResult(
		&stdout,
		&stderr,
		preflightResult{
			Status:   "failed",
			Issues:   []string{"z", "a"},
			Warnings: []string{"two", "one"},
		},
		1,
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	var result preflightResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Issues, ",") != "a,z" ||
		strings.Join(result.Warnings, ",") != "one,two" {
		t.Fatalf("unsorted result = %#v", result)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = emitResult(
		failingWriter{err: errors.New("write failed")},
		&stderr,
		preflightResult{Status: "failed"},
		0,
	)
	if exitCode != 1 ||
		!strings.Contains(stderr.String(), "preflight output could not be encoded") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
