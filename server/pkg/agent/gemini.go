package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// geminiBackend implements Backend by spawning the Gemini CLI
// with --output-format stream-json.
type geminiBackend struct {
	cfg Config
}

func (b *geminiBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "gemini"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("gemini executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	args := buildGeminiArgs(prompt, opts)

	cmd := exec.CommandContext(runCtx, execPath, args...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildGeminiEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gemini stdout pipe: %w", err)
	}
	cmd.Stderr = newLogWriter(b.cfg.Logger, "[gemini:stderr] ")

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	b.cfg.Logger.Info("gemini started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		var output strings.Builder
		var sessionID string
		finalStatus := "completed"
		var finalError string
		usage := make(map[string]TokenUsage)

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var msg geminiStreamMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "init":
				sessionID = msg.SessionID
				trySend(msgCh, Message{Type: MessageStatus, Status: "running"})

			case "message":
				if msg.Role == "model" {
					// Model response text
					if msg.Content != "" {
						output.WriteString(msg.Content)
						trySend(msgCh, Message{Type: MessageText, Content: msg.Content})
					}
				}

			case "tool_call":
				var input map[string]any
				if msg.Input != nil {
					_ = json.Unmarshal(msg.Input, &input)
				}
				trySend(msgCh, Message{
					Type:   MessageToolUse,
					Tool:   msg.ToolName,
					CallID: msg.CallID,
					Input:  input,
				})

			case "tool_result":
				trySend(msgCh, Message{
					Type:   MessageToolResult,
					CallID: msg.CallID,
					Output: msg.Output,
				})

			case "thinking":
				if msg.Content != "" {
					trySend(msgCh, Message{Type: MessageThinking, Content: msg.Content})
				}

			case "result":
				if msg.SessionID != "" {
					sessionID = msg.SessionID
				}
				// Result status
				if msg.Status == "error" || msg.Status == "failed" {
					finalStatus = "failed"
					if msg.Error != nil {
						finalError = msg.Error.Message
					}
				} else if msg.Status == "completed" || msg.Status == "" {
					finalStatus = "completed"
				}
				// Result text
				if msg.ResultText != "" {
					output.Reset()
					output.WriteString(msg.ResultText)
				}
				// Token usage from stats
				if msg.Stats != nil {
					b.handleStats(msg.Stats, usage)
				}

			case "log":
				if msg.Content != "" {
					trySend(msgCh, Message{
						Type:    MessageLog,
						Level:   msg.Level,
						Content: msg.Content,
					})
				}
			}
		}

		// Wait for process exit
		exitErr := cmd.Wait()
		duration := time.Since(startTime)

		if runCtx.Err() == context.DeadlineExceeded {
			finalStatus = "timeout"
			finalError = fmt.Sprintf("gemini timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			finalStatus = "aborted"
			finalError = "execution cancelled"
		} else if exitErr != nil && finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("gemini exited with error: %v", exitErr)
		}

		b.cfg.Logger.Info("gemini finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		resCh <- Result{
			Status:     finalStatus,
			Output:     output.String(),
			Error:      finalError,
			DurationMs: duration.Milliseconds(),
			SessionID:  sessionID,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

func (b *geminiBackend) handleStats(stats *geminiStats, usage map[string]TokenUsage) {
	// Aggregate per-model stats
	for modelName, modelStats := range stats.Models {
		u := usage[modelName]
		u.InputTokens += modelStats.InputTokens
		u.OutputTokens += modelStats.OutputTokens
		u.CacheReadTokens += modelStats.Cached
		usage[modelName] = u
	}

	// Fallback: if no per-model stats, use top-level stats with a generic model name
	if len(stats.Models) == 0 && (stats.InputTokens > 0 || stats.OutputTokens > 0) {
		u := usage["gemini"]
		u.InputTokens += stats.InputTokens
		u.OutputTokens += stats.OutputTokens
		u.CacheReadTokens += stats.Cached
		usage["gemini"] = u
	}
}

// ── Gemini CLI stream-json types ──

type geminiStreamMessage struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp,omitempty"`
	SessionID string          `json:"session_id,omitempty"`

	// init fields
	Model string `json:"model,omitempty"`

	// message fields
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`

	// tool_call fields
	ToolName string          `json:"tool_name,omitempty"`
	CallID   string          `json:"call_id,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`

	// result fields
	Status     string          `json:"status,omitempty"`
	ResultText string          `json:"result,omitempty"`
	Error      *geminiError    `json:"error,omitempty"`
	Stats      *geminiStats    `json:"stats,omitempty"`

	// log fields
	Level string `json:"level,omitempty"`
}

type geminiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type geminiStats struct {
	TotalTokens  int64                    `json:"total_tokens"`
	InputTokens  int64                    `json:"input_tokens"`
	OutputTokens int64                    `json:"output_tokens"`
	Cached       int64                    `json:"cached"`
	Input        int64                    `json:"input"`
	DurationMs   int64                    `json:"duration_ms"`
	ToolCalls    int                      `json:"tool_calls"`
	Models       map[string]geminiModelStats `json:"models,omitempty"`
}

type geminiModelStats struct {
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Cached       int64 `json:"cached"`
	Input        int64 `json:"input"`
}

// ── Arg builder ──

func buildGeminiArgs(prompt string, opts ExecOptions) []string {
	args := []string{
		"-p", prompt,
		"-o", "stream-json",
		"--yolo",
	}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.SystemPrompt != "" {
		// Gemini CLI uses policy for system-level instructions
		// prepend it to the prompt as a workaround
		args[1] = opts.SystemPrompt + "\n\n---\n\n" + prompt
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	return args
}

func buildGeminiEnv(extra map[string]string) []string {
	return mergeEnv(os.Environ(), extra)
}
