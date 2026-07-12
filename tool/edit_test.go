package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditSingleReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"world","new_string":"there"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "1 replacement") {
		t.Fatalf("Content = %q, want singular replacement note", res.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there" {
		t.Fatalf("file = %q, want %q", got, "hello there")
	}
}

func TestEditReplaceAllCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"a","new_string":"b","replace_all":true}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "3 replacements") {
		t.Fatalf("Content = %q, want 3 replacements", res.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "b b b" {
		t.Fatalf("file = %q, want %q", got, "b b b")
	}
}

func TestEditUniquenessViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"a","new_string":"b"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (non-unique match)")
	}
	if !strings.Contains(res.Content, "not unique") {
		t.Fatalf("Content = %q, want not-unique message", res.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a a a" {
		t.Fatalf("file was modified: %q", got)
	}
}

func TestEditNoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"missing","new_string":"x"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (no match)")
	}
	if !strings.Contains(res.Content, "not found") {
		t.Fatalf("Content = %q, want not-found message", res.Content)
	}
}

func TestEditIdenticalStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"world","new_string":"world"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (identical strings)")
	}
	if !strings.Contains(res.Content, "identical") {
		t.Fatalf("Content = %q, want identical message", res.Content)
	}
}

func TestEditEmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"","new_string":"x"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (empty old_string)")
	}
	if !strings.Contains(res.Content, "must not be empty") {
		t.Fatalf("Content = %q, want empty-old_string message", res.Content)
	}
}

func TestEditMissingFile(t *testing.T) {
	dir := t.TempDir()
	e := NewEdit(dir)
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"missing.txt","old_string":"a","new_string":"b"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (missing file)")
	}
	if !strings.Contains(res.Content, "file not found") {
		t.Fatalf("Content = %q, want file-not-found message", res.Content)
	}
}

func TestEditMissingPath(t *testing.T) {
	dir := t.TempDir()
	e := NewEdit(dir)
	_, err := e.Run(context.Background(), json.RawMessage(`{"old_string":"a","new_string":"b"}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing path")
	}
}
