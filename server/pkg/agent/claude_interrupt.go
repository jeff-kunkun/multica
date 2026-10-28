package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// claudeInterrupter stops the running Claude turn and continues the same
// session with a new user message (DENE-2667). Stream-json input stays open
// for the whole run: an interrupt control request ends the turn (killing a
// running Bash command) with a result event, and the next user frame on the
// same stdin starts the next turn in the same process and session. Verified
// against Claude Code 2.1.311.
//
// The stdout reader owns the turn boundary: it asks resultSeen whether a
// result ends the run or makes room for the waiting message.
type claudeInterrupter struct {
	w io.Writer

	mu      sync.Mutex
	open    bool // the initial prompt was written and no final result arrived
	ended   bool
	seq     int
	pending *claudeInterrupt
}

type claudeInterrupt struct {
	requestID string
	text      string
	done      chan error
	resulted  bool // the interrupted turn's result arrived; the message is being written
}

func newClaudeInterrupter(w io.Writer) *claudeInterrupter {
	return &claudeInterrupter{w: w}
}

// started opens admission once the initial prompt is on stdin.
func (c *claudeInterrupter) started() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ended {
		c.open = true
	}
}

func (c *claudeInterrupter) interrupt(ctx context.Context, text string) error {
	c.mu.Lock()
	switch {
	case c.ended:
		c.mu.Unlock()
		return context.Canceled
	case !c.open:
		c.mu.Unlock()
		return errors.New("claude turn has not started")
	case c.pending != nil:
		c.mu.Unlock()
		return errors.New("claude interrupt already in progress")
	}
	c.seq++
	in := &claudeInterrupt{requestID: fmt.Sprintf("multica-interrupt-%d", c.seq), text: text, done: make(chan error, 1)}
	c.pending = in
	c.mu.Unlock()

	if err := writeClaudeFrame(c.w, map[string]any{
		"type": "control_request", "request_id": in.requestID,
		"request": map[string]any{"subtype": "interrupt"},
	}); err != nil {
		c.settle(in, err)
		return err
	}
	select {
	case err := <-in.done:
		return err
	case <-ctx.Done():
	}
	// A message already being written decides its own outcome.
	c.mu.Lock()
	writing := in.resulted
	if !writing && c.pending == in {
		c.pending = nil
	}
	c.mu.Unlock()
	if writing {
		return <-in.done
	}
	return ctx.Err()
}

// resultSeen is called by the stdout reader for every result event. A nil
// return means the run is over; otherwise the caller runs the returned write
// off the reader goroutine and keeps stdin open for the next turn.
func (c *claudeInterrupter) resultSeen() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := c.pending
	if in == nil || in.resulted || c.ended {
		c.open = false
		return nil
	}
	// Whether this result is the interrupted turn's or a turn that finished
	// just before the interrupt landed, the message starts the next turn.
	in.resulted = true
	return func() {
		err := writeClaudeInput(c.w, in.text)
		c.settle(in, err)
	}
}

// controlResponse fails a pending interrupt Claude refused.
func (c *claudeInterrupter) controlResponse(raw json.RawMessage) {
	var response struct {
		RequestID string `json:"request_id"`
		Subtype   string `json:"subtype"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Subtype == "success" {
		return
	}
	c.mu.Lock()
	in := c.pending
	refused := in != nil && in.requestID == response.RequestID && !in.resulted
	c.mu.Unlock()
	if refused {
		c.settle(in, fmt.Errorf("claude refused the interrupt: %s", response.Error))
	}
}

func (c *claudeInterrupter) settle(in *claudeInterrupt, err error) {
	c.mu.Lock()
	if c.pending == in {
		c.pending = nil
	}
	c.mu.Unlock()
	select {
	case in.done <- err:
	default:
	}
}

// end closes admission when stdin closes; a waiting interrupt fails.
func (c *claudeInterrupter) end() {
	c.mu.Lock()
	c.ended, c.open = true, false
	in := c.pending
	waiting := in != nil && !in.resulted
	c.mu.Unlock()
	if waiting {
		c.settle(in, context.Canceled)
	}
}
