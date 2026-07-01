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
	ExitCode int    `json:"exitCode"`
}

type OutputStore interface {
	LoadTerminalCWD() string
	SaveTerminalCWD(cwd string)
	SaveTerminalOutput(outputID, stdout, stderr string, exitCode int)
	ReadTerminalOutputFields(outputID string) (stdout, stderr string, exitCode int, ok bool)
}

// Service 在时间和输出限制内执行 shell 命令。
//
// 该实现对应 TS terminal 能力：维护 cwd 状态、
// 按 ID 捕获输出，并允许之后读取而无需重新运行命令。
type Service struct {
	mu      sync.Mutex
	cfg     Config
	cwd     string
	outputs map[string]Output
	store   OutputStore
	running bool
}

// NewService 规范化终端默认值，并返回可用服务。
func NewService(cfg Config, store OutputStore) (*Service, error) {
	if cfg.InitialCwd == "" {
		home, _ := os.UserHomeDir()
		cfg.InitialCwd = filepath.Join(home, "kagami")
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.Shell == "" && runtime.GOOS == "windows" {
		cfg.Shell = "powershell"
	}
	if cfg.Shell == "" {
		cfg.Shell = "/bin/sh"
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
	cwd := cfg.InitialCwd
	if store != nil {
		if persisted := strings.TrimSpace(store.LoadTerminalCWD()); persisted != "" {
			cwd = persisted
		}
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, cwd: cwd, outputs: map[string]Output{}, store: store}, nil
}

// Run 执行一条 shell 命令并保存捕获输出。
func (s *Service) Run(ctx context.Context, command string) (Output, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Output{}, fmt.Errorf("命令不能为空")
	}
	if len(command) > s.cfg.MaxCommandLength {
		return Output{}, fmt.Errorf("命令太长")
	}
	if nextCwd, ok, err := s.resolveCD(command); ok {
		if err != nil {
			return Output{}, err
		}
		s.mu.Lock()
		s.cwd = nextCwd
		s.mu.Unlock()
		if s.store != nil {
			s.store.SaveTerminalCWD(nextCwd)
		}
		out := Output{OutputID: time.Now().Format("20060102150405.000000000"), Stdout: nextCwd + "\n", ExitCode: 0}
		if s.store != nil {
			s.store.SaveTerminalOutput(out.OutputID, out.Stdout, out.Stderr, out.ExitCode)
		}
		return out, nil
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return Output{}, fmt.Errorf("已有 bash 命令正在运行")
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimeout)
	defer cancel()
	cmd := shellCommand(runCtx, s.cfg.Shell, command)
	s.mu.Lock()
	cmd.Dir = s.cwd
	s.mu.Unlock()
	var stdout, stderr limitBuffer
	stdout.limit = s.cfg.MaxOutputBytes
	stderr.limit = s.cfg.MaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	out := Output{OutputID: time.Now().Format("20060102150405.000000000"), Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
	s.mu.Lock()
	s.outputs[out.OutputID] = out
	s.mu.Unlock()
	if s.store != nil {
		s.store.SaveTerminalOutput(out.OutputID, out.Stdout, out.Stderr, out.ExitCode)
	}
	return out, err
}

// Read 按 ID 返回之前捕获的输出。
func (s *Service) Read(outputID string) (Output, bool) {
	s.mu.Lock()
	out, ok := s.outputs[outputID]
	if !ok {
		s.mu.Unlock()
		if s.store == nil {
			return Output{}, false
		}
		stdout, stderr, exitCode, ok := s.store.ReadTerminalOutputFields(outputID)
		if !ok {
			return Output{}, false
		}
		out = Output{OutputID: outputID, Stdout: stdout, Stderr: stderr, ExitCode: exitCode}
		s.mu.Lock()
		s.outputs[outputID] = out
		s.mu.Unlock()
	} else {
		s.mu.Unlock()
	}
	out.Stdout = trimBytes(out.Stdout, s.cfg.ReadOutputMaxSize)
	out.Stderr = trimBytes(out.Stderr, s.cfg.ReadOutputMaxSize)
	return out, true
}

func (s *Service) CWD() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *Service) resolveCD(command string) (string, bool, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] != "cd" {
		return "", false, nil
	}
	target := ""
	if len(fields) > 1 {
		target = strings.Join(fields[1:], " ")
	}
	target = strings.Trim(target, `"'`)
	if target == "" || target == "~" {
		home, _ := os.UserHomeDir()
		target = home
	}
	s.mu.Lock()
	base := s.cwd
	s.mu.Unlock()
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil {
		return "", true, err
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("不是目录：%s", target)
	}
	return target, true, nil
}

func shellCommand(ctx context.Context, shell, command string) *exec.Cmd {
	base := strings.ToLower(filepath.Base(shell))
	if strings.Contains(base, "powershell") {
		return exec.CommandContext(ctx, shell, "-NoProfile", "-Command", command)
	}
	if base == "cmd" || base == "cmd.exe" {
		return exec.CommandContext(ctx, shell, "/C", command)
	}
	return exec.CommandContext(ctx, shell, "-lc", command)
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
