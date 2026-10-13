package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewUsesDiffFilesAndProjectContext(t *testing.T) {
	root := testProject(t)
	if err := os.WriteFile(filepath.Join(root, "service.go"), []byte("package service\nfunc Count() int { return 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "test"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "service.go"), []byte("package service\nfunc Count() int { return 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	client := &fakeChat{}
	answer, err := (&agent{client: client}).review(context.Background(), root, "HEAD^", "HEAD", func(string) {})
	if err != nil || answer == "" {
		t.Fatalf("review: %q, %v", answer, err)
	}
	if len(client.seen) != 1 || !strings.Contains(client.seen[0][1].Content, "service.go") || !strings.Contains(client.seen[0][1].Content, "+func Count() int { return 2 }") {
		t.Fatal("diff or file list missing")
	}
	contextText := strings.Split(client.seen[0][1].Content, "Контекст RAG:")[1]
	if !strings.Contains(contextText, "docs/architecture.md:") || !strings.Contains(contextText, "service.go:") {
		t.Fatal("review must receive both documentation and code RAG context")
	}
	if _, err := (&agent{client: client}).review(context.Background(), root, "--bad", "HEAD", func(string) {}); err == nil {
		t.Fatal("unsafe ref accepted")
	}
}

func TestIndexPrioritizesChangedCodeAndReservesDocumentation(t *testing.T) {
	root := testProject(t)
	for i := 0; i < 110; i++ {
		name := fmt.Sprintf("a%03d.go", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "z.go"), []byte("package fixture\nfunc Changed() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	s := searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json"), preferred: map[string]bool{"z.go": true}}
	if _, err := s.build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	idx, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, chunk := range idx.Chunks {
		seen[chunk.Source] = true
	}
	if !seen["z.go"] || !seen["README.md"] || !seen["docs/architecture.md"] || len(seen) != 102 {
		t.Fatal("index cap displaced changed code or documentation")
	}
}

func TestReviewUsesMergeBaseInsteadOfTargetTip(t *testing.T) {
	root := testProject(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		git("add", ".")
		git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	}
	git("init", "-b", "base")
	commit()
	git("checkout", "-b", "feature")
	write("feature.go", "package fixture\nfunc Feature() {}\n")
	commit()
	head := git("rev-parse", "HEAD")
	git("checkout", "base")
	write("target_only.go", "package fixture\nfunc TargetOnly() {}\n")
	commit()
	git("checkout", "feature")
	client := &fakeChat{}
	if _, err := (&agent{client: client}).review(context.Background(), root, "base", head, func(string) {}); err != nil {
		t.Fatal(err)
	}
	prompt := client.seen[0][1].Content
	if strings.Contains(prompt, "target_only.go") || !strings.Contains(prompt, "+func Feature() {}") {
		t.Fatal("review included target branch changes outside the PR")
	}
}

func TestReviewContextWithoutDocsDirectory(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"README.md":  "# Project\nArchitecture and usage guidelines.",
		"service.go": "package project\nfunc UniqueOperation() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := searchIndex{root: root, path: filepath.Join(t.TempDir(), "index.json")}
	if _, err := s.build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	hits, err := s.reviewContext("UniqueOperation")
	if err != nil || len(hits) != 2 {
		t.Fatalf("documentation fallback and code retrieval: %v, %v", hits, err)
	}
	if hits[0].Source != "README.md" || hits[1].Source != "service.go" {
		t.Fatal("missing documentation or code")
	}
}
