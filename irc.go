package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// IRC event type constants
const (
	CONNECTED    = "001"
	PRIVMSG      = "PRIVMSG"
	JOIN         = "JOIN"
	PART         = "PART"
	DISCONNECTED = "DISCONNECTED"
)

// Line represents a parsed IRC message
type Line struct {
	Nick string
	Src  string
	Cmd  string
	Args []string
}

// Config holds IRC connection configuration
type Config struct {
	Nick      string
	User      string
	RealName  string
	Server    string
	SSL       bool
	SSLConfig *tls.Config
	NewNick   func(string) string
}

// NewConfig creates a new IRC configuration with defaults
func NewConfig(nick string) *Config {
	return &Config{
		Nick:     nick,
		User:     nick,
		RealName: nick,
		NewNick: func(n string) string {
			return n + "_"
		},
	}
}

// Conn represents an IRC connection
type Conn struct {
	cfg         *Config
	conn        net.Conn
	connected   bool
	mu          sync.RWMutex
	handlers    map[string][]func(*Conn, *Line)
	reader      *bufio.Reader
	writer      *bufio.Writer
	quit        chan struct{}
	done        chan struct{}
	debugSendFn func(string) // Callback for debug logging sent messages
}

// Client creates a new IRC connection from config
func Client(cfg *Config) *Conn {
	return &Conn{
		cfg:      cfg,
		handlers: make(map[string][]func(*Conn, *Line)),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// HandleFunc registers a handler for a specific IRC event
func (c *Conn) HandleFunc(event string, handler func(*Conn, *Line)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[event] = append(c.handlers[event], handler)
}

// SetDebugSend sets a callback function for debug logging of sent messages
func (c *Conn) SetDebugSend(fn func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.debugSendFn = fn
}

// Connect establishes connection to the IRC server
func (c *Conn) Connect() error {
	var conn net.Conn
	var err error

	if c.cfg.SSL {
		conn, err = tls.Dial("tcp", c.cfg.Server, c.cfg.SSLConfig)
	} else {
		conn, err = net.Dial("tcp", c.cfg.Server)
	}

	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.conn = conn
	c.reader = bufio.NewReader(conn)
	c.writer = bufio.NewWriter(conn)

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()

	// Send initial IRC handshake (must be after setting connected=true)
	c.sendRaw(fmt.Sprintf("NICK %s", c.cfg.Nick))
	c.sendRaw(fmt.Sprintf("USER %s 0 * :%s", c.cfg.User, c.cfg.RealName))

	// Start read loop
	go c.readLoop()

	return nil
}

// Connected returns whether the connection is active
func (c *Conn) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// readLoop continuously reads from the IRC server
func (c *Conn) readLoop() {
	defer close(c.done)
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.dispatch("DISCONNECTED", &Line{Cmd: "DISCONNECTED"})
	}()

	for {
		select {
		case <-c.quit:
			return
		default:
			// Set read deadline to allow checking quit channel
			c.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

			line, err := c.reader.ReadString('\n')
			if err != nil {
				// Check if it's a timeout
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// Connection closed or error
				return
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			parsed := c.parseLine(line)
			c.dispatch(parsed.Cmd, parsed)
		}
	}
}

// parseLine parses an IRC protocol line
func (c *Conn) parseLine(raw string) *Line {
	line := &Line{Args: []string{}}

	// Handle prefix (source)
	if strings.HasPrefix(raw, ":") {
		parts := strings.SplitN(raw[1:], " ", 2)
		if len(parts) < 2 {
			return line
		}
		line.Src = parts[0]

		// Extract nick from source (nick!user@host)
		if idx := strings.Index(line.Src, "!"); idx != -1 {
			line.Nick = line.Src[:idx]
		}

		raw = parts[1]
	}

	// Parse command and parameters
	parts := strings.Split(raw, " ")
	if len(parts) == 0 {
		return line
	}

	line.Cmd = strings.ToUpper(parts[0])
	parts = parts[1:]

	// Parse arguments
	for i := 0; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], ":") {
			// Trailing parameter (contains the rest of the message)
			line.Args = append(line.Args, strings.Join(parts[i:], " ")[1:])
			break
		}
		line.Args = append(line.Args, parts[i])
	}

	return line
}

// dispatch calls all registered handlers for an event
func (c *Conn) dispatch(event string, line *Line) {
	// Handle PING automatically FIRST (before any handlers)
	if event == "PING" {
		var pongCmd string
		if len(line.Args) > 0 {
			pongCmd = fmt.Sprintf("PONG :%s", line.Args[0])
		} else {
			// Some servers send PING without arguments
			pongCmd = "PONG"
		}
		// Send PONG synchronously to ensure it goes out immediately
		if err := c.sendRaw(pongCmd); err != nil {
			// Log error but continue
			fmt.Fprintf(os.Stderr, "Failed to send PONG: %v\n", err)
		}
	}

	c.mu.RLock()
	// Get specific event handlers
	handlers := c.handlers[event]
	// Get wildcard handlers
	wildcardHandlers := c.handlers["*"]
	c.mu.RUnlock()

	// Call specific event handlers
	for _, handler := range handlers {
		go handler(c, line)
	}

	// Call wildcard handlers for all events
	for _, handler := range wildcardHandlers {
		go handler(c, line)
	}
}

// sendRaw sends a raw IRC command
func (c *Conn) sendRaw(cmd string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected {
		return fmt.Errorf("not connected")
	}

	// Call debug callback if set
	if c.debugSendFn != nil {
		c.debugSendFn(cmd)
	}

	_, err := c.writer.WriteString(cmd + "\r\n")
	if err != nil {
		return err
	}
	return c.writer.Flush()
}

// Raw sends a raw IRC command
func (c *Conn) Raw(cmd string) {
	c.sendRaw(cmd)
}

// Join joins an IRC channel
func (c *Conn) Join(channel string) {
	c.sendRaw(fmt.Sprintf("JOIN %s", channel))
}

// Part leaves an IRC channel
func (c *Conn) Part(channel string) {
	c.sendRaw(fmt.Sprintf("PART %s", channel))
}

// Privmsg sends a PRIVMSG to a target (channel or user)
func (c *Conn) Privmsg(target, message string) {
	c.sendRaw(fmt.Sprintf("PRIVMSG %s :%s", target, message))
}

// Quit disconnects from the IRC server with a quit message
func (c *Conn) Quit(message string) {
	if message == "" {
		c.sendRaw("QUIT")
	} else {
		c.sendRaw(fmt.Sprintf("QUIT :%s", message))
	}

	close(c.quit)

	// Wait for disconnect or timeout
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}

	if c.conn != nil {
		c.conn.Close()
	}
}
