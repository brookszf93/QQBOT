package terminal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Config 控制命令执行的安全限制和 shell 行为。
type Config struct {
	InitialCwd        string
	CommandTimeout    time.Duration
	PreviewBytes      int
	MaxOutputBytes    int
	MaxCommandLength  int
	ReadOutputMaxSize int
	Shell             string
}

// Output 是已完成命令捕获到的 stdout/stderr。
type Output struct {
	OutputID string `json:"outputId"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Service 在时间和输出限制内执行 shell 命令。
//
// 按 ID 捕获输出，并允许之后读取而无需重新运行命令。
type Service struct {
	mu      sync.Mutex
	cfg     Config
	cwd     string
	outputs map[string]Output
}

// NewService 规范化终端默认值，并返回可用服务。
func NewService(cfg Config) (*Service, error) {
	if cfg.InitialCwd == "" {
		home, _ := os.UserHomeDir()
		cfg.InitialCwd = filepath.Join(home, "qqbot-ai")
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.Shell == "" || (runtime.GOOS == "windows" && cfg.Shell == "/bin/sh") {
		if runtime.GOOS == "windows" {
			cfg.Shell = "powershell"
		} else {
			cfg.Shell = "/bin/sh"
		}
	}
	if cfg.PreviewBytes <= 0 {
		cfg.PreviewBytes = 2048
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 1 << 20
	}
	if cfg.MaxCommandLength <= 0 {
		cfg.MaxCommandLength = 4096
	}
	if cfg.ReadOutputMaxSize <= 0 {
		cfg.ReadOutputMaxSize = 4096
	}
	return &Service{cfg: cfg, cwd: cfg.InitialCwd, outputs: map[string]Output{}}, nil
}

// Run 执行一条 shell 命令并保存捕获输出。
func (s *Service) Run(ctx context.Context, command string) (Output, error) {
	if len(command) > s.cfg.MaxCommandLength {
		return Output{}, fmt.Errorf("command too long")
	}
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimeout)
	defer cancel()
	args := []string{"-lc", command}
	if runtime.GOOS == "windows" || strings.Contains(strings.ToLower(filepath.Base(s.cfg.Shell)), "powershell") {
		args = []string{"-NoProfile", "-Command", command}
	}
	cmd := exec.CommandContext(runCtx, s.cfg.Shell, args...)
	s.mu.Lock()
	cmd.Dir = s.cwd
	s.mu.Unlock()
	var stdout, stderr limitBuffer
	stdout.limit = s.cfg.MaxOutputBytes
	stderr.limit = s.cfg.MaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := Output{OutputID: time.Now().Format("20060102150405.000000000"), Stdout: stdout.String(), Stderr: stderr.String()}
	s.mu.Lock()
	s.outputs[out.OutputID] = out
	s.mu.Unlock()
	return out, err
}

// Read 按 ID 返回之前捕获的输出。
func (s *Service) Read(outputID string) (Output, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, ok := s.outputs[outputID]
	if !ok {
		return Output{}, false
	}
	out.Stdout = trimBytes(out.Stdout, s.cfg.ReadOutputMaxSize)
	out.Stderr = trimBytes(out.Stderr, s.cfg.ReadOutputMaxSize)
	return out, true
}

type limitBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.Len() >= b.limit {
		return len(p), nil
	}
	remain := b.limit - b.Len()
	if len(p) > remain {
		p = p[:remain]
	}
	return b.Buffer.Write(p)
}

func trimBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
