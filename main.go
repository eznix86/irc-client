package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	accent = lipgloss.Color("#5EEAD4") // teal
	muted  = lipgloss.Color("#9CA3AF")
	userA  = lipgloss.Color("#EAB308") // yellow
	userB  = lipgloss.Color("#A78BFA") // purple
	userC  = lipgloss.Color("#34D399") // green

	borderColor   = lipgloss.Color("#374151") // gray separators
	sidebarWidth  = 22
	sidebarBorder = 2 // 1 left + 1 right
	chatBorder    = 2 // 1 left + 1 right

	sidebarBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Width(sidebarWidth)

	sidebarFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(accent).
				Width(sidebarWidth)

	chatBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)

	chatFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(accent)

	channelStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)

	userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#93C5FD"))

	msgTs   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FDE68A"))
	msgUser = lipgloss.NewStyle().Bold(true)
	msgBody = lipgloss.NewStyle()

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Foreground(lipgloss.Color("#D1D5DB"))

	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1F2937")).
			Foreground(lipgloss.Color("#9CA3AF")).
			Padding(0, 1)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB")).
			Bold(true)

	statusTimeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB")).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")).
			Bold(true)

	debugStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// IRCEventHandler processes IRC events
type IRCEventHandler func(m *model, eventType string, data map[string]string)

// CommandHandler processes user commands
type CommandHandler func(m *model, args []string) error

// ─────────────────────────── MODEL ───────────────────────────
type focusArea int

const (
	focusInput focusArea = iota
	focusChannels
	focusChat
	focusUsers
)

// ircMessage represents a typed IRC event message
type ircMessage struct {
	Type      string
	Timestamp string
	Data      map[string]string
}

type model struct {
	width, height int

	channelsView viewport.Model // Left sidebar - channels list
	chat         viewport.Model // Center - chat messages
	usersView    viewport.Model // Right sidebar - users list
	input        textinput.Model

	messages        []string
	channelUsers    map[string][]string // Users per channel
	channels        []string
	channel         string
	currentTime     time.Time
	currentFocus    focusArea
	selectedChanIdx int
	selectedUserIdx int
	showHelp        bool

	inputHistory []string // Command history
	historyIndex int      // Current position in history (-1 = not browsing)
	historyTemp  string   // Temporary storage for current input when browsing history

	irc        *Conn
	nick       string
	ircMsgChan chan ircMessage
	server     string
	port       string
	useTLS     bool
	verbose    bool // Debug mode

	// Handler registries
	ircEventHandlers map[string]IRCEventHandler
	commandHandlers  map[string]CommandHandler
}

type tickMsg time.Time

func initialModel(server, port, nick string, verbose bool) model {
	inp := textinput.New()
	inp.Placeholder = "Type a message..."
	inp.Prompt = "> "
	inp.Width = 100
	inp.Focus()

	channelsView := viewport.New(sidebarWidth, 20)
	chat := viewport.New(80, 20)
	usersView := viewport.New(sidebarWidth, 20)

	// Determine if we should use TLS based on common TLS ports
	useTLS := port == "6697" || port == "7000" || port == "7001" || port == "9999"

	// Setup IRC connection
	cfg := NewConfig(nick)
	cfg.SSL = useTLS
	cfg.Server = fmt.Sprintf("%s:%s", server, port)
	cfg.NewNick = func(n string) string { return n + "_" }

	// Configure TLS
	if useTLS {
		cfg.SSLConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	c := Client(cfg)
	msgChan := make(chan ircMessage, 100)

	m := model{
		messages:         []string{},
		channelUsers:     make(map[string][]string),
		channels:         []string{},
		channel:          "",
		channelsView:     channelsView,
		chat:             chat,
		usersView:        usersView,
		input:            inp,
		currentTime:      time.Now(),
		currentFocus:     focusInput,
		selectedChanIdx:  0,
		selectedUserIdx:  0,
		inputHistory:     []string{},
		historyIndex:     -1,
		historyTemp:      "",
		irc:              c,
		nick:             nick,
		ircMsgChan:       msgChan,
		server:           server,
		port:             port,
		useTLS:           useTLS,
		verbose:          verbose,
		ircEventHandlers: make(map[string]IRCEventHandler),
		commandHandlers:  make(map[string]CommandHandler),
	}

	// Setup IRC and command handlers
	m.registerIRCEventHandlers()
	m.setupIRCHandlers()
	m.setupCommandHandlers()

	m.updateSidebars()
	m.updateChat()
	return m
}

func (m *model) setupIRCHandlers() {
	// Helper to send typed messages
	send := func(msgType string, data map[string]string) {
		m.ircMsgChan <- ircMessage{
			Type:      msgType,
			Timestamp: time.Now().Format("15:04"),
			Data:      data,
		}
	}

	// Wildcard handler for debug mode - logs all IRC events
	if m.verbose {
		// Log received messages
		m.irc.HandleFunc("*", func(conn *Conn, line *Line) {
			msg := fmt.Sprintf("RECV CMD=%s NICK=%s SRC=%s ARGS=%v", line.Cmd, line.Nick, line.Src, line.Args)
			send("DEBUG", map[string]string{"message": msg})
		})

		// Log sent messages
		m.irc.SetDebugSend(func(cmd string) {
			msg := fmt.Sprintf("SEND %s", cmd)
			send("DEBUG", map[string]string{"message": msg})
		})
	}

	m.irc.HandleFunc("001", func(conn *Conn, line *Line) {
		send("CONNECTED", map[string]string{"message": "Connected to server"})
	})

	m.irc.HandleFunc("PRIVMSG", func(conn *Conn, line *Line) {
		if len(line.Args) >= 2 {
			send("PRIVMSG", map[string]string{
				"nick":    line.Nick,
				"target":  line.Args[0],
				"message": line.Args[1],
			})
		}
	})

	m.irc.HandleFunc("JOIN", func(conn *Conn, line *Line) {
		if len(line.Args) >= 1 {
			send("JOIN", map[string]string{
				"nick":    line.Nick,
				"channel": line.Args[0],
			})
		}
	})

	m.irc.HandleFunc("PART", func(conn *Conn, line *Line) {
		if len(line.Args) >= 1 {
			send("PART", map[string]string{
				"nick":    line.Nick,
				"channel": line.Args[0],
			})
		}
	})

	m.irc.HandleFunc("QUIT", func(conn *Conn, line *Line) {
		data := map[string]string{"nick": line.Nick}
		if len(line.Args) >= 1 {
			data["reason"] = line.Args[0]
		}
		send("QUIT", data)
	})

	m.irc.HandleFunc("353", func(conn *Conn, line *Line) { // RPL_NAMREPLY
		if len(line.Args) >= 4 {
			send("NAMES", map[string]string{
				"channel": line.Args[2],
				"users":   line.Args[3],
			})
		}
	})

	m.irc.HandleFunc("366", func(conn *Conn, line *Line) { // RPL_ENDOFNAMES
		if len(line.Args) >= 2 {
			send("ENDOFNAMES", map[string]string{
				"channel": line.Args[1],
			})
		}
	})

	m.irc.HandleFunc("322", func(conn *Conn, line *Line) { // RPL_LIST
		if len(line.Args) >= 4 {
			send("LIST", map[string]string{
				"channel": line.Args[1],
				"users":   line.Args[2],
				"topic":   line.Args[3],
			})
		}
	})

	m.irc.HandleFunc("323", func(conn *Conn, line *Line) { // RPL_LISTEND
		send("LISTEND", map[string]string{"message": "End of channel list"})
	})

	m.irc.HandleFunc("321", func(conn *Conn, line *Line) { // RPL_LISTSTART
		send("LISTSTART", map[string]string{"message": "Channel list:"})
	})

	// Helper for server info messages (002, 003, 004, 005, 251-255, 265-266)
	sendServerInfo := func(conn *Conn, line *Line) {
		if len(line.Args) > 0 {
			send("SERVER_INFO", map[string]string{
				"message": strings.Join(line.Args, " "),
			})
		}
	}

	m.irc.HandleFunc("001", func(conn *Conn, line *Line) { // RPL_WELCOME
		if len(line.Args) > 0 {
			send("WELCOME", map[string]string{
				"message": strings.Join(line.Args, " "),
			})
		}
	})

	// RPL_YOURHOST, RPL_CREATED, RPL_MYINFO, RPL_ISUPPORT
	m.irc.HandleFunc("002", sendServerInfo)
	m.irc.HandleFunc("003", sendServerInfo)
	m.irc.HandleFunc("004", sendServerInfo)
	m.irc.HandleFunc("005", sendServerInfo)

	// Helper for MOTD messages
	sendMOTD := func(conn *Conn, line *Line) {
		if len(line.Args) > 0 {
			send("MOTD", map[string]string{
				"message": strings.Join(line.Args, " "),
			})
		}
	}

	// RPL_MOTDSTART, RPL_MOTD, RPL_ENDOFMOTD
	m.irc.HandleFunc("375", sendMOTD)
	m.irc.HandleFunc("372", sendMOTD)
	m.irc.HandleFunc("376", sendMOTD)

	// RPL_LUSERCLIENT, RPL_LUSERME, RPL_LOCALUSERS, RPL_GLOBALUSERS
	m.irc.HandleFunc("251", sendServerInfo)
	m.irc.HandleFunc("255", sendServerInfo)
	m.irc.HandleFunc("265", sendServerInfo)
	m.irc.HandleFunc("266", sendServerInfo)

	m.irc.HandleFunc("NOTICE", func(conn *Conn, line *Line) {
		if len(line.Args) > 1 {
			sender := line.Nick
			if sender == "" && line.Src != "" {
				sender = line.Src
			}
			send("NOTICE", map[string]string{
				"sender":  sender,
				"message": line.Args[1],
			})
		}
	})

	// Handle IRC errors
	sendError := func(message string) {
		send("ERROR", map[string]string{"message": message})
	}

	m.irc.HandleFunc("DISCONNECTED", func(conn *Conn, line *Line) {
		sendError("Disconnected from server")
	})

	m.irc.HandleFunc("ERROR", func(conn *Conn, line *Line) {
		errMsg := "Unknown error"
		if len(line.Args) > 0 {
			errMsg = strings.Join(line.Args, " ")
		}
		// Check for TLS mismatch errors
		if strings.Contains(errMsg, "TLS") || strings.Contains(errMsg, "SSL") {
			send("TLS_ERROR", map[string]string{"message": errMsg})
		} else {
			sendError(errMsg)
		}
	})

	// Errors that indicate channel/nick doesn't exist (should be removed)
	permanentErrors := map[string]bool{
		"401": true, // ERR_NOSUCHNICK
		"403": true, // ERR_NOSUCHCHANNEL
	}

	// Helper to send error with optional channel context
	sendChannelError := func(errorCode, message, channel string) {
		data := map[string]string{
			"message": message,
			"channel": channel,
		}
		// Only remove for permanent errors where channel/nick doesn't exist
		if permanentErrors[errorCode] {
			data["remove_channel"] = "true"
		}
		send("ERROR", data)
	}

	// Handle common IRC error codes with descriptive messages
	errorHandlers := map[string]func(*Conn, *Line){
		"401": func(conn *Conn, line *Line) { // ERR_NOSUCHNICK
			if len(line.Args) >= 2 {
				nick := line.Args[1]
				sendChannelError("401", fmt.Sprintf("No such nick: %s", nick), nick)
			}
		},
		"403": func(conn *Conn, line *Line) { // ERR_NOSUCHCHANNEL
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("403", fmt.Sprintf("No such channel: %s", channel), channel)
			}
		},
		"404": func(conn *Conn, line *Line) { // ERR_CANNOTSENDTOCHAN
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("404", fmt.Sprintf("Cannot send to channel: %s", channel), channel)
			}
		},
		"415": func(conn *Conn, line *Line) { // Cannot send to channel (need NickServ)
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				message := strings.Join(line.Args[1:], " ")
				sendChannelError("415", message, channel)
			}
		},
		"433": func(conn *Conn, line *Line) { // ERR_NICKNAMEINUSE
			if len(line.Args) >= 2 {
				sendError(fmt.Sprintf("Nickname already in use: %s", line.Args[1]))
			}
		},
		"451": func(conn *Conn, line *Line) { // ERR_NOTREGISTERED
			if len(line.Args) >= 1 {
				sendError(strings.Join(line.Args, " "))
			} else {
				sendError("You have not registered")
			}
		},
		"471": func(conn *Conn, line *Line) { // ERR_CHANNELISFULL
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("471", fmt.Sprintf("Channel is full: %s", channel), channel)
			}
		},
		"473": func(conn *Conn, line *Line) { // ERR_INVITEONLYCHAN
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("473", fmt.Sprintf("Channel is invite-only: %s", channel), channel)
			}
		},
		"474": func(conn *Conn, line *Line) { // ERR_BANNEDFROMCHAN
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("474", fmt.Sprintf("Banned from channel: %s", channel), channel)
			}
		},
		"475": func(conn *Conn, line *Line) { // ERR_BADCHANNELKEY
			if len(line.Args) >= 2 {
				channel := line.Args[1]
				sendChannelError("475", fmt.Sprintf("Bad channel key: %s", channel), channel)
			}
		},
	}

	// Generic errors that just forward the message (without channel context)
	genericErrorCodes := []string{"477", "489", "520"}
	for _, code := range genericErrorCodes {
		m.irc.HandleFunc(code, func(conn *Conn, line *Line) {
			if len(line.Args) >= 2 {
				sendError(strings.Join(line.Args[1:], " "))
			}
		})
	}

	// Register all error handlers
	for code, handler := range errorHandlers {
		m.irc.HandleFunc(code, handler)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitForIRCMessage(msgChan chan ircMessage) tea.Cmd {
	return func() tea.Msg {
		return <-msgChan
	}
}

func connectIRC(ircConn *Conn) tea.Cmd {
	return func() tea.Msg {
		if err := ircConn.Connect(); err != nil {
			return ircMessage{
				Type:      "ERROR",
				Timestamp: time.Now().Format("15:04"),
				Data:      map[string]string{"message": err.Error()},
			}
		}
		return nil
	}
}

func (m *model) reconnectWithoutTLS() tea.Cmd {
	return func() tea.Msg {
		// Disconnect current connection if any
		if m.irc != nil {
			m.irc.Quit("")
		}

		// Create new connection without TLS
		cfg := NewConfig(m.nick)
		cfg.SSL = false
		cfg.Server = fmt.Sprintf("%s:%s", m.server, m.port)
		cfg.NewNick = func(n string) string { return n + "_" }
		cfg.SSLConfig = nil

		m.irc = Client(cfg)
		m.useTLS = false
		m.setupIRCHandlers()

		// Connect
		if err := m.irc.Connect(); err != nil {
			return ircMessage{
				Type:      "ERROR",
				Timestamp: time.Now().Format("15:04"),
				Data:      map[string]string{"message": err.Error()},
			}
		}
		return ircMessage{
			Type:      "RECONNECTED",
			Timestamp: time.Now().Format("15:04"),
			Data:      map[string]string{"message": "Reconnected without TLS"},
		}
	}
}

// ─────────────────────────── HELPERS ───────────────────────────
func (m *model) fmtMsg(ts, channel, user, body string) string {
	var uStyle lipgloss.Style
	switch strings.ToLower(user) {
	case "alice":
		uStyle = msgUser.Foreground(userA)
	case "erin":
		uStyle = msgUser.Foreground(userB)
	case "dave":
		uStyle = msgUser.Foreground(userC)
	default:
		uStyle = msgUser.Foreground(lipgloss.Color("#60A5FA"))
	}

	// Calculate available width: chat width - timestamp (5) - channel (~10) - spaces (3) - username (~15)
	tsWidth := 5
	chanWidth := 10
	userWidth := 15
	availableWidth := m.chat.Width - tsWidth - chanWidth - 3 - userWidth
	if availableWidth < 20 {
		availableWidth = 20
	}

	chanStyle := lipgloss.NewStyle().Foreground(muted).Width(chanWidth)
	bodyStyle := msgBody.Width(availableWidth)

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		msgTs.Render(ts),
		" ",
		chanStyle.Render(channel),
		" ",
		uStyle.Render(capFirst(user)),
		" ",
		bodyStyle.Render(body),
	)
}

func (m *model) fmtSys(ts, body string) string {
	// Calculate available width: chat width - timestamp (5) - space (1)
	availableWidth := m.chat.Width - 6
	if availableWidth < 20 {
		availableWidth = 20
	}

	b := lipgloss.NewStyle().Foreground(accent).Width(availableWidth)
	return lipgloss.JoinHorizontal(lipgloss.Left, msgTs.Render(ts), " ", b.Render(body))
}

func (m *model) fmtErr(ts, body string) string {
	// Calculate available width: chat width - timestamp (5) - space (1)
	availableWidth := m.chat.Width - 6
	if availableWidth < 20 {
		availableWidth = 20
	}

	errStyleWithWidth := errorStyle.Width(availableWidth)
	return lipgloss.JoinHorizontal(lipgloss.Left, msgTs.Render(ts), " ", errStyleWithWidth.Render("✗ "+body))
}

func (m *model) fmtDebug(ts, body string) string {
	// Calculate available width: chat width - timestamp (5) - space (1) - [debug] (8)
	availableWidth := m.chat.Width - 14
	if availableWidth < 20 {
		availableWidth = 20
	}

	debugStyleWithWidth := debugStyle.Width(availableWidth)
	return lipgloss.JoinHorizontal(lipgloss.Left, msgTs.Render(ts), " ", debugStyle.Render("[debug]"), " ", debugStyleWithWidth.Render(body))
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// truncateString truncates a string to maxLen with ellipsis if needed
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// truncateAndPad truncates and right-pads a string to exactly width characters
func truncateAndPad(s string, width int) string {
	if len(s) > width {
		if width <= 3 {
			return s[:width]
		}
		s = s[:width-3] + "..."
	}
	// Pad with spaces to maintain column alignment
	if len(s) < width {
		s = s + strings.Repeat(" ", width-len(s))
	}
	return s
}

// updateSidebars updates both channel and user sidebars and recalculates layout
func (m *model) updateSidebars() {
	m.updateChannelsView()
	m.updateUsersView()
	// Recalculate layout when sidebars change
	m.handleWindowResize(tea.WindowSizeMsg{Width: m.width, Height: m.height})
}

// removeChannel removes a channel from the channels list
func (m *model) removeChannel(channel string) {
	// Remove from channels list
	newChannels := make([]string, 0, len(m.channels))
	for _, ch := range m.channels {
		if ch != channel {
			newChannels = append(newChannels, ch)
		}
	}
	m.channels = newChannels

	// Remove users for this channel
	delete(m.channelUsers, channel)

	// If we were in this channel, switch to another or clear
	if m.channel == channel {
		if len(m.channels) > 0 {
			m.channel = m.channels[0]
			m.selectedChanIdx = 0
			promptStyle := lipgloss.NewStyle().Foreground(accent)
			m.input.Prompt = promptStyle.Render(fmt.Sprintf("[%s]", m.channel)) + " > "
		} else {
			m.channel = ""
			m.input.Prompt = "> "
		}
	}

	// Adjust selectedChanIdx if needed
	if m.selectedChanIdx >= len(m.channels) && len(m.channels) > 0 {
		m.selectedChanIdx = len(m.channels) - 1
	}

	m.updateSidebars()
}

// setFocus changes the focus to a new area and updates UI accordingly
func (m *model) setFocus(newFocus focusArea) {
	if m.currentFocus == newFocus {
		return
	}

	m.currentFocus = newFocus

	// Handle input field focus
	if newFocus == focusInput {
		m.input.Focus()
	} else {
		m.input.Blur()
	}

	m.updateSidebars()
}

func (m *model) updateChannelsView() {
	var b strings.Builder

	// Separate channels (start with #) from private messages (don't start with #)
	var channels []string
	var privateMessages []string
	for _, ch := range m.channels {
		if strings.HasPrefix(ch, "#") {
			channels = append(channels, ch)
		} else {
			privateMessages = append(privateMessages, ch)
		}
	}

	// Show channels section
	if len(channels) > 0 {
		b.WriteString(channelStyle.Render("CHANNELS") + "\n")
		for _, ch := range channels {
			i := m.findChannelIndex(ch)
			var line string
			if i == m.selectedChanIdx && m.currentFocus == focusChannels {
				// Selected and focused
				selectedStyle := lipgloss.NewStyle().Foreground(accent).Background(lipgloss.Color("235")).Bold(true)
				line = selectedStyle.Render("► " + ch)
			} else if ch == m.channel {
				// Current active channel
				activeStyle := lipgloss.NewStyle().Foreground(accent)
				line = activeStyle.Render("• " + ch)
			} else {
				// Regular channel
				line = "  " + ch
			}
			b.WriteString(line + "\n")
		}
	}

	// Show private messages section (only if there are any)
	if len(privateMessages) > 0 {
		if len(channels) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(channelStyle.Render("MESSAGES") + "\n")
		for _, ch := range privateMessages {
			i := m.findChannelIndex(ch)
			var line string
			if i == m.selectedChanIdx && m.currentFocus == focusChannels {
				// Selected and focused
				selectedStyle := lipgloss.NewStyle().Foreground(accent).Background(lipgloss.Color("235")).Bold(true)
				line = selectedStyle.Render("► " + ch)
			} else if ch == m.channel {
				// Current active channel
				activeStyle := lipgloss.NewStyle().Foreground(accent)
				line = activeStyle.Render("• " + ch)
			} else {
				// Regular private message
				line = "  " + ch
			}
			b.WriteString(line + "\n")
		}
	}

	m.channelsView.SetContent(b.String())
}

// findChannelIndex returns the index of a channel in m.channels
func (m *model) findChannelIndex(channel string) int {
	for i, ch := range m.channels {
		if ch == channel {
			return i
		}
	}
	return -1
}

func (m *model) updateUsersView() {
	var b strings.Builder

	// Show users for current channel
	if m.channel != "" {
		if users, ok := m.channelUsers[m.channel]; ok && len(users) > 0 {
			userCount := len(users)
			plural := "people"
			if userCount == 1 {
				plural = "person"
			}
			b.WriteString(channelStyle.Render(fmt.Sprintf("%d %s here", userCount, plural)) + "\n")
			for i, u := range users {
				var line string
				if i == m.selectedUserIdx && m.currentFocus == focusUsers {
					// Selected and focused
					selectedStyle := lipgloss.NewStyle().Foreground(accent).Background(lipgloss.Color("235")).Bold(true)
					line = selectedStyle.Render("► " + u)
				} else {
					line = "  " + userStyle.Render(u)
				}
				b.WriteString(line + "\n")
			}
		}
	}

	m.usersView.SetContent(b.String())
}

func (m *model) updateChat() {
	m.chat.SetContent(strings.Join(m.messages, "\n"))
	m.chat.GotoBottom()
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), connectIRC(m.irc), waitForIRCMessage(m.ircMsgChan))
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func (m *model) addMessage(msg string) {
	m.messages = append(m.messages, msg)
	// Keep last 500 messages to prevent memory leak
	if len(m.messages) > 500 {
		m.messages = m.messages[len(m.messages)-500:]
	}
	m.updateChat()
}

func (m *model) handleIRCMessage(msg ircMessage) {
	// Use timestamp from message if present, otherwise use current time
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().Format("15:04")
	}

	// Dispatch to handler
	if handler, ok := m.ircEventHandlers[msg.Type]; ok {
		handler(m, msg.Type, msg.Data)
	}
}

func (m *model) handleCommand(input string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	if handler, ok := m.commandHandlers[cmd]; ok {
		if err := handler(m, args); err != nil {
			ts := time.Now().Format("15:04")
			m.addMessage(m.fmtErr(ts, err.Error()))
		}
	} else {
		ts := time.Now().Format("15:04")
		m.addMessage(m.fmtErr(ts, fmt.Sprintf("Unknown command: %s", cmd)))
	}
}

func (m *model) handleWindowResize(msg tea.WindowSizeMsg) {
	m.width, m.height = msg.Width, msg.Height

	// Account for input box (1 line + 2 borders = 3 rows) + status bar (1 row)
	inputBoxHeight := 3
	statusBarHeight := 1
	availableHeight := m.height - inputBoxHeight - statusBarHeight

	// Check which sidebars are visible
	hasChannelsOrMessages := len(m.channels) > 0
	hasUsers := m.channel != "" && len(m.channelUsers[m.channel]) > 0

	// Calculate total width used by sidebars
	channelsTotalWidth := 0
	if hasChannelsOrMessages {
		channelsTotalWidth = sidebarWidth + sidebarBorder
	}

	usersTotalWidth := 0
	if hasUsers {
		usersTotalWidth = sidebarWidth + sidebarBorder
	}

	// Chat panel gets whatever remains (or full width if no sidebars)
	chatTotalWidth := m.width - channelsTotalWidth - usersTotalWidth
	if chatTotalWidth < 20 {
		chatTotalWidth = 20
	}

	// Set viewport dimensions (content area, excluding borders)
	m.channelsView.Width = sidebarWidth
	m.channelsView.Height = max(1, availableHeight-sidebarBorder)

	m.chat.Width = chatTotalWidth - chatBorder
	m.chat.Height = max(1, availableHeight-chatBorder)

	m.usersView.Width = sidebarWidth
	m.usersView.Height = max(1, availableHeight-sidebarBorder)

	// Set input width (account for borders and prompt)
	m.input.Width = m.width - 6
}

func (m *model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	// Handle left click for panel selection
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
		// Calculate panel boundaries
		channelsTotalWidth := sidebarWidth + sidebarBorder
		usersTotalWidth := sidebarWidth + sidebarBorder

		x := msg.X
		y := msg.Y

		// Ignore clicks in status bar and input area
		inputBoxHeight := 3
		statusBarHeight := 1
		contentHeight := m.height - inputBoxHeight - statusBarHeight

		if y >= contentHeight {
			// Click in input or status bar - don't change focus
			return nil
		}

		// Determine which panel was clicked
		if x < channelsTotalWidth {
			m.setFocus(focusChannels)
		} else if x >= m.width-usersTotalWidth {
			m.setFocus(focusUsers)
		} else {
			m.setFocus(focusChat)
		}
		return nil
	}

	// Handle mouse wheel scrolling
	if msg.Action == tea.MouseActionPress {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			switch m.currentFocus {
			case focusChat:
				m.chat.ScrollUp(3)
			case focusUsers:
				m.usersView.ScrollUp(3)
			case focusChannels:
				m.channelsView.ScrollUp(3)
			}
			return nil
		case tea.MouseButtonWheelDown:
			switch m.currentFocus {
			case focusChat:
				m.chat.ScrollDown(3)
			case focusUsers:
				m.usersView.ScrollDown(3)
			case focusChannels:
				m.channelsView.ScrollDown(3)
			}
			return nil
		}
	}

	return nil
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "f1":
		m.showHelp = !m.showHelp
		return nil
	case "ctrl+c":
		if m.irc != nil {
			m.irc.Quit("Goodbye!")
		}
		return tea.Quit
	case "tab":
		// Cycle focus: input -> channels -> chat -> users -> input
		switch m.currentFocus {
		case focusInput:
			m.setFocus(focusChannels)
		case focusChannels:
			m.setFocus(focusChat)
		case focusChat:
			m.setFocus(focusUsers)
		case focusUsers:
			m.setFocus(focusInput)
		}
		return nil
	case "esc":
		m.setFocus(focusInput)
		return nil
	case "up":
		if m.currentFocus == focusChannels && len(m.channels) > 0 {
			if m.selectedChanIdx > 0 {
				m.selectedChanIdx--
			}
			m.updateSidebars()
			return nil
		} else if m.currentFocus == focusUsers && m.channel != "" {
			if users, ok := m.channelUsers[m.channel]; ok && len(users) > 0 {
				if m.selectedUserIdx > 0 {
					m.selectedUserIdx--
				}
				m.usersView.ScrollUp(1)
				m.updateUsersView()
			}
			return nil
		} else if m.currentFocus == focusChat {
			m.chat.ScrollUp(1)
			return nil
		} else if m.currentFocus == focusInput && len(m.inputHistory) > 0 {
			// Navigate backward in history
			if m.historyIndex == -1 {
				// First time browsing history, save current input
				m.historyTemp = m.input.Value()
				m.historyIndex = len(m.inputHistory) - 1
			} else if m.historyIndex > 0 {
				m.historyIndex--
			}
			if m.historyIndex >= 0 && m.historyIndex < len(m.inputHistory) {
				m.input.SetValue(m.inputHistory[m.historyIndex])
				m.input.CursorEnd()
			}
			return nil
		}
	case "k":
		if m.currentFocus == focusChannels && len(m.channels) > 0 {
			if m.selectedChanIdx > 0 {
				m.selectedChanIdx--
			}
			m.updateSidebars()
			return nil
		} else if m.currentFocus == focusChat {
			m.chat.ScrollUp(1)
			return nil
		}
	case "down":
		if m.currentFocus == focusChannels && len(m.channels) > 0 {
			if m.selectedChanIdx < len(m.channels)-1 {
				m.selectedChanIdx++
			}
			m.updateSidebars()
			return nil
		} else if m.currentFocus == focusUsers && m.channel != "" {
			if users, ok := m.channelUsers[m.channel]; ok && len(users) > 0 {
				if m.selectedUserIdx < len(users)-1 {
					m.selectedUserIdx++
				}
				m.usersView.ScrollDown(1)
				m.updateUsersView()
			}
			return nil
		} else if m.currentFocus == focusChat {
			m.chat.ScrollDown(1)
			return nil
		} else if m.currentFocus == focusInput && m.historyIndex != -1 {
			// Navigate forward in history
			if m.historyIndex < len(m.inputHistory)-1 {
				m.historyIndex++
				m.input.SetValue(m.inputHistory[m.historyIndex])
				m.input.CursorEnd()
			} else {
				// Reached the end, restore temp input
				m.historyIndex = -1
				m.input.SetValue(m.historyTemp)
				m.input.CursorEnd()
			}
			return nil
		}
	case "j":
		if m.currentFocus == focusChannels && len(m.channels) > 0 {
			if m.selectedChanIdx < len(m.channels)-1 {
				m.selectedChanIdx++
			}
			m.updateSidebars()
			return nil
		} else if m.currentFocus == focusChat {
			m.chat.ScrollDown(1)
			return nil
		}
	case "pgup", "b":
		if m.currentFocus == focusChat {
			m.chat.PageUp()
			return nil
		}
	case "pgdown", "f":
		if m.currentFocus == focusChat {
			m.chat.PageDown()
			return nil
		}
	case "home", "g":
		if m.currentFocus == focusChat {
			m.chat.GotoTop()
			return nil
		}
	case "end", "G":
		if m.currentFocus == focusChat {
			m.chat.GotoBottom()
			return nil
		}
	case "enter":
		if m.currentFocus == focusChannels && len(m.channels) > 0 {
			// Switch to selected channel
			selectedChannel := m.channels[m.selectedChanIdx]
			m.channel = selectedChannel
			m.selectedUserIdx = 0 // Reset user selection when changing channels

			// Update input prompt to show current channel with accent color
			promptStyle := lipgloss.NewStyle().Foreground(accent)
			m.input.Prompt = promptStyle.Render(fmt.Sprintf("[%s]", m.channel)) + " > "

			m.setFocus(focusInput)
			return nil
		} else if m.currentFocus == focusUsers && m.channel != "" {
			// User selected from users list - populate /msg command
			if users, ok := m.channelUsers[m.channel]; ok && len(users) > 0 && m.selectedUserIdx < len(users) {
				selectedUser := users[m.selectedUserIdx]
				m.input.SetValue(fmt.Sprintf("/msg %s ", selectedUser))
				m.input.CursorEnd()
				m.setFocus(focusInput)
			}
			return nil
		} else if m.currentFocus == focusInput && m.input.Value() != "" {
			input := strings.TrimSpace(m.input.Value())

			// Add to history (avoid duplicates of last command)
			if input != "" {
				if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != input {
					m.inputHistory = append(m.inputHistory, input)
					// Keep only last 100 commands
					if len(m.inputHistory) > 100 {
						m.inputHistory = m.inputHistory[1:]
					}
				}
			}
			// Reset history navigation
			m.historyIndex = -1
			m.historyTemp = ""

			if strings.HasPrefix(input, "/") {
				m.handleCommand(input)
			} else if m.channel != "" {
				// Send message to channel
				ts := time.Now().Format("15:04")

				// Check if IRC connection is alive
				if m.irc == nil {
					m.addMessage(m.fmtErr(ts, "IRC connection is nil!"))
				} else if !m.irc.Connected() {
					m.addMessage(m.fmtErr(ts, "Not connected to IRC server!"))
				} else {
					// Send via IRC
					m.irc.Privmsg(m.channel, input)

					// Display locally (most IRC servers don't echo your own messages)
					m.addMessage(m.fmtMsg(ts, m.channel, m.nick, input))
				}
			} else {
				m.addMessage(m.fmtErr(time.Now().Format("15:04"), "Not in a channel. Use /join <channel> first"))
			}
			m.input.SetValue("")
		}
		return nil
	}

	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.handleWindowResize(msg)
		return m, nil

	case tickMsg:
		m.currentTime = time.Time(msg)
		return m, tickCmd()

	case ircMessage:
		// Check if this is a reconnect request
		if msg.Type == "RECONNECT" {
			return m, m.reconnectWithoutTLS()
		}
		m.handleIRCMessage(msg)
		return m, waitForIRCMessage(m.ircMsgChan)

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case tea.KeyMsg:
		cmd := m.handleKey(msg)
		if cmd != nil {
			return m, cmd
		}
	}

	// Only update input if it's focused
	if m.currentFocus == focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) renderHelp() string {
	helpTitle := lipgloss.NewStyle().
		Foreground(accent).
		Bold(true)

	helpKey := lipgloss.NewStyle().
		Foreground(accent).
		Bold(true)

	helpContent := helpTitle.Render("IRC Client Help") + "\n\n" +
		helpKey.Render("Navigation:") + "\n" +
		"  Tab           Cycle focus (Input → Sidebar → Chat)\n" +
		"  Esc           Return focus to input\n" +
		"  ↑/↓ or j/k    Scroll chat (when focused) or navigate channels (sidebar)\n" +
		"  PgUp/PgDn     Scroll chat by page\n" +
		"  Home/End      Jump to top/bottom of chat\n" +
		"  Mouse wheel   Scroll chat\n\n" +
		helpKey.Render("Channels:") + "\n" +
		"  Enter         Switch to selected channel (when sidebar focused)\n" +
		"  ►             Selected channel indicator\n" +
		"  •             Active channel indicator\n\n" +
		helpKey.Render("Commands:") + "\n" +
		"  /join <channel>       Join a channel\n" +
		"  /part                 Leave current channel\n" +
		"  /msg <nick> <msg>     Send private message\n" +
		"  /list                 List all channels\n" +
		"  /quit                 Disconnect\n\n" +
		helpKey.Render("Other:") + "\n" +
		"  F1            Toggle this help\n" +
		"  Ctrl+C        Quit application\n"

	helpBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(1, 2).
		Width(m.width - 4).
		Height(m.height - 4)

	return helpBox.Render(helpContent)
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	// Show help overlay if requested
	if m.showHelp {
		return m.renderHelp()
	}

	// Check if sidebars should be shown
	hasChannelsOrMessages := len(m.channels) > 0
	hasUsers := m.channel != "" && len(m.channelUsers[m.channel]) > 0

	// Build the row components
	var components []string

	// Left sidebar (channels and messages)
	if hasChannelsOrMessages {
		channelsContent := m.channelsView.View()
		channelsBox := m.renderSidebarBox(channelsContent, m.channelsView.Height, m.currentFocus == focusChannels)
		components = append(components, channelsBox)
	}

	// Center (chat) - always shown
	chatContent := m.chat.View()
	chatBox := m.renderChatBox(chatContent, m.currentFocus == focusChat)
	components = append(components, chatBox)

	// Right sidebar (users)
	if hasUsers {
		usersContent := m.usersView.View()
		usersBox := m.renderSidebarBox(usersContent, m.usersView.Height, m.currentFocus == focusUsers)
		components = append(components, usersBox)
	}

	// Compose the UI
	row := lipgloss.JoinHorizontal(lipgloss.Top, components...)
	inputBox := inputBoxStyle.Width(m.width - 2).Render(m.input.View())
	statusBar := m.renderStatusBar()

	return lipgloss.JoinVertical(lipgloss.Left, row, inputBox, statusBar)
}

// renderSidebarBox applies border styling to sidebar content
func (m model) renderSidebarBox(content string, height int, focused bool) string {
	if focused {
		return sidebarFocusedStyle.Height(height).Render(content)
	}
	return sidebarBoxStyle.Height(height).Render(content)
}

// renderChatBox applies border styling to chat content
func (m model) renderChatBox(content string, focused bool) string {
	if focused {
		return chatFocusedStyle.
			Width(m.chat.Width).
			Height(m.chat.Height).
			Render(content)
	}
	return chatBoxStyle.
		Width(m.chat.Width).
		Height(m.chat.Height).
		Render(content)
}

// getStatusHelpText returns the help text for the current focus area
func (m model) getStatusHelpText() string {
	// Pre-rendered help text based on focus (these rarely change)
	switch m.currentFocus {
	case focusInput:
		return statusKeyStyle.Render("↑↓") + " history • " +
			statusKeyStyle.Render("Enter") + " send • " +
			statusKeyStyle.Render("Tab") + " focus • " +
			statusKeyStyle.Render("F1") + " help"
	case focusChannels:
		return statusKeyStyle.Render("↑↓") + " navigate • " +
			statusKeyStyle.Render("Enter") + " join • " +
			statusKeyStyle.Render("Tab") + " focus • " +
			statusKeyStyle.Render("F1") + " help"
	case focusChat:
		return statusKeyStyle.Render("↑↓") + " scroll • " +
			statusKeyStyle.Render("PgUp/PgDn") + " page • " +
			statusKeyStyle.Render("Tab") + " focus • " +
			statusKeyStyle.Render("F1") + " help"
	case focusUsers:
		return statusKeyStyle.Render("↑↓") + " navigate • " +
			statusKeyStyle.Render("Enter") + " /msg • " +
			statusKeyStyle.Render("Tab") + " focus • " +
			statusKeyStyle.Render("F1") + " help"
	default:
		return ""
	}
}

func (m model) renderStatusBar() string {
	helpText := m.getStatusHelpText()
	clock := statusTimeStyle.Render(m.currentTime.Format("15:04"))

	// Calculate widths
	availableWidth := m.width - 2
	clockWidth := lipgloss.Width(clock)
	helpWidth := availableWidth - clockWidth

	// Place help on left, clock on right
	leftSide := lipgloss.PlaceHorizontal(helpWidth, lipgloss.Left, helpText)
	rightSide := lipgloss.PlaceHorizontal(clockWidth, lipgloss.Right, clock)

	content := lipgloss.JoinHorizontal(lipgloss.Top, leftSide, rightSide)

	return lipgloss.NewStyle().Width(m.width).Render(content)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

//
// ─────────────────────────── HANDLER REGISTRATION ───────────────────────────
//

func (m *model) registerIRCEventHandlers() {
	// Register IRC event handlers - use shared handler for system messages
	m.ircEventHandlers["CONNECTED"] = handleSystemMessage
	m.ircEventHandlers["WELCOME"] = handleSystemMessage
	m.ircEventHandlers["SERVER_INFO"] = handleSystemMessage
	m.ircEventHandlers["LISTSTART"] = handleSystemMessage
	m.ircEventHandlers["LISTEND"] = handleSystemMessage
	m.ircEventHandlers["RECONNECTED"] = handleSystemMessage

	m.ircEventHandlers["MOTD"] = handleMOTD
	m.ircEventHandlers["NOTICE"] = handleNotice
	m.ircEventHandlers["PRIVMSG"] = handlePrivmsg
	m.ircEventHandlers["JOIN"] = handleJoin
	m.ircEventHandlers["PART"] = handlePart
	m.ircEventHandlers["QUIT"] = handleQuit
	m.ircEventHandlers["NAMES"] = handleNames
	m.ircEventHandlers["ENDOFNAMES"] = handleEndOfNames
	m.ircEventHandlers["LIST"] = handleList
	m.ircEventHandlers["ERROR"] = handleError
	m.ircEventHandlers["TLS_ERROR"] = handleTLSError
	m.ircEventHandlers["RECONNECT"] = handleReconnect
	m.ircEventHandlers["DEBUG"] = handleDebug
}

func (m *model) setupCommandHandlers() {
	// Register command handlers
	m.commandHandlers["/join"] = cmdJoin
	m.commandHandlers["/part"] = cmdPart
	m.commandHandlers["/quit"] = cmdQuit
	m.commandHandlers["/list"] = cmdList
	m.commandHandlers["/msg"] = cmdMsg
}


// handleSystemMessage handles generic system messages (CONNECTED, WELCOME, SERVER_INFO, etc.)
func handleSystemMessage(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtSys(ts, data["message"]))
}

func handleMOTD(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	availableWidth := m.chat.Width - 6
	if availableWidth < 20 {
		availableWidth = 20
	}
	motdStyle := lipgloss.NewStyle().Foreground(muted).Width(availableWidth)
	m.addMessage(lipgloss.JoinHorizontal(lipgloss.Left, msgTs.Render(ts), " ", motdStyle.Render(data["message"])))
}

func handleNotice(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	sender := data["sender"]
	message := data["message"]
	availableWidth := m.chat.Width - 6
	if availableWidth < 20 {
		availableWidth = 20
	}
	noticeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Italic(true).Width(availableWidth)
	m.addMessage(lipgloss.JoinHorizontal(lipgloss.Left, msgTs.Render(ts), " ", noticeStyle.Render(fmt.Sprintf("[%s] %s", sender, message))))
}

func handlePrivmsg(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	nick := data["nick"]
	target := data["target"]
	message := data["message"]

	var displayTarget string

	if target == m.nick {
		// Private message TO us
		displayTarget = nick
		if !contains(m.channels, nick) {
			m.channels = append(m.channels, nick)
			m.updateSidebars()
		}
	} else if strings.HasPrefix(target, "#") {
		// Regular channel message
		displayTarget = target
		if users, ok := m.channelUsers[target]; ok {
			if !contains(users, nick) {
				m.channelUsers[target] = append(users, nick)
				if target == m.channel {
					m.updateSidebars()
				}
			}
		} else {
			m.channelUsers[target] = []string{nick}
			if target == m.channel {
				m.updateSidebars()
			}
		}
	} else {
		// Message we sent to someone else
		displayTarget = target
		if !contains(m.channels, target) {
			m.channels = append(m.channels, target)
			m.updateSidebars()
		}
	}

	m.addMessage(m.fmtMsg(ts, displayTarget, nick, message))
}

func handleJoin(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	nick := data["nick"]
	channel := data["channel"]

	if nick == m.nick {
		m.channel = channel
		if !contains(m.channels, channel) {
			m.channels = append(m.channels, channel)
		}
		// Update input prompt
		promptStyle := lipgloss.NewStyle().Foreground(accent)
		m.input.Prompt = promptStyle.Render(fmt.Sprintf("[%s]", m.channel)) + " > "
	} else {
		// Add user to channel user list
		if users, ok := m.channelUsers[channel]; ok {
			if !contains(users, nick) {
				m.channelUsers[channel] = append(users, nick)
			}
		} else {
			m.channelUsers[channel] = []string{nick}
		}
	}
	m.updateSidebars()
	m.addMessage(m.fmtSys(ts, fmt.Sprintf("%s joined %s", nick, channel)))
}

func handleQuit(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	nick := data["nick"]
	reason := data["reason"]

	// Remove user from all channels
	for channel := range m.channelUsers {
		if users, ok := m.channelUsers[channel]; ok {
			newUsers := []string{}
			for _, u := range users {
				if u != nick {
					newUsers = append(newUsers, u)
				}
			}
			m.channelUsers[channel] = newUsers
		}
	}

	m.updateSidebars()

	// Display quit message with reason in parentheses
	if reason != "" {
		m.addMessage(m.fmtSys(ts, fmt.Sprintf("%s has quit (%s)", nick, reason)))
	} else {
		m.addMessage(m.fmtSys(ts, fmt.Sprintf("%s has quit", nick)))
	}
}

func handlePart(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	nick := data["nick"]
	channel := data["channel"]

	if nick == m.nick {
		// Remove from channels list
		newChannels := []string{}
		for _, ch := range m.channels {
			if ch != channel {
				newChannels = append(newChannels, ch)
			}
		}
		m.channels = newChannels

		// Clear current channel if it's the one we left
		if m.channel == channel {
			if len(m.channels) > 0 {
				m.channel = m.channels[0]
				m.selectedChanIdx = 0
				promptStyle := lipgloss.NewStyle().Foreground(accent)
				m.input.Prompt = promptStyle.Render(fmt.Sprintf("[%s]", m.channel)) + " > "
			} else {
				m.channel = ""
				m.input.Prompt = "> "
			}
		}

		// Remove users for this channel
		delete(m.channelUsers, channel)
	} else {
		// Someone else left
		if users, ok := m.channelUsers[channel]; ok {
			newUsers := []string{}
			for _, u := range users {
				if u != nick {
					newUsers = append(newUsers, u)
				}
			}
			m.channelUsers[channel] = newUsers
		}
	}
	m.updateSidebars()
	m.addMessage(m.fmtSys(ts, fmt.Sprintf("%s left %s", nick, channel)))
}

func handleNames(m *model, eventType string, data map[string]string) {
	channel := data["channel"]
	names := strings.Split(data["users"], " ")

	// Append to existing list (353 can come in multiple messages)
	if existing, ok := m.channelUsers[channel]; ok {
		m.channelUsers[channel] = append(existing, names...)
	} else {
		m.channelUsers[channel] = names
	}
	// Don't update sidebar yet - wait for end of names (366)
}

func handleEndOfNames(m *model, eventType string, data map[string]string) {
	channel := data["channel"]

	// Remove mode prefixes (@, +, etc.) and deduplicate
	if users, ok := m.channelUsers[channel]; ok {
		cleaned := make(map[string]bool)
		cleanedList := []string{}

		for _, user := range users {
			// Remove IRC mode prefixes (@, +, %, ~, &)
			cleanUser := strings.TrimLeft(user, "@+%~&")
			if cleanUser != "" && !cleaned[cleanUser] {
				cleaned[cleanUser] = true
				cleanedList = append(cleanedList, cleanUser)
			}
		}

		m.channelUsers[channel] = cleanedList
	}

	m.updateSidebars()
}

func handleList(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	channel := data["channel"]
	users := data["users"]
	topic := data["topic"]

	chatWidth := m.chat.Width
	if chatWidth <= 0 {
		chatWidth = 80
	}

	tsWidth := 5
	chanWidth := 20
	userWidth := 8
	spacing := 4
	availableWidth := chatWidth - tsWidth - chanWidth - userWidth - spacing
	if availableWidth < 10 {
		availableWidth = 10
	}

	listStyle := lipgloss.NewStyle().Foreground(accent)
	userStyle := lipgloss.NewStyle().Foreground(muted)
	topicStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

	// Build the line with proper alignment - don't use Width() to avoid wrapping
	// Manually truncate and pad to maintain single-line layout
	channelPadded := truncateAndPad(channel, chanWidth)
	usersPadded := truncateAndPad(fmt.Sprintf("[%s]", users), userWidth)
	topicTruncated := truncateString(topic, availableWidth)

	m.addMessage(lipgloss.JoinHorizontal(
		lipgloss.Left,
		msgTs.Render(ts),
		" ",
		listStyle.Render(channelPadded),
		" ",
		userStyle.Render(usersPadded),
		" ",
		topicStyle.Render(topicTruncated),
	))
}

func handleError(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	message := data["message"]
	m.addMessage(m.fmtErr(ts, message))

	// Only remove channel for errors that mean it doesn't exist or is permanently inaccessible
	// Don't remove for temporary permission issues (like needing to register with NickServ)
	if shouldRemove := data["remove_channel"]; shouldRemove == "true" {
		if channel := data["channel"]; channel != "" {
			m.removeChannel(channel)
		}
	}
}

func handleTLSError(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtErr(ts, data["message"]))
	if m.useTLS {
		m.addMessage(m.fmtSys(ts, "Attempting to reconnect without TLS..."))
		go func() {
			time.Sleep(500 * time.Millisecond)
			m.ircMsgChan <- ircMessage{
				Type:      "RECONNECT",
				Timestamp: time.Now().Format("15:04"),
				Data:      make(map[string]string),
			}
		}()
	}
}

func handleReconnect(m *model, eventType string, data map[string]string) {
	// Handled in Update() to trigger reconnection command
}

func handleDebug(m *model, eventType string, data map[string]string) {
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtDebug(ts, data["message"]))
}


func cmdJoin(m *model, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("Usage: /join <channel>")
	}
	channel := args[0]
	if !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}
	m.irc.Join(channel)
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtSys(ts, fmt.Sprintf("Joining %s...", channel)))
	return nil
}

func cmdPart(m *model, args []string) error {
	if m.channel == "" {
		return fmt.Errorf("Not in a channel")
	}
	channelToLeave := m.channel
	m.irc.Part(channelToLeave)
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtSys(ts, fmt.Sprintf("Leaving %s...", channelToLeave)))
	return nil
}

func cmdQuit(m *model, args []string) error {
	m.irc.Quit("Goodbye!")
	return nil
}

func cmdList(m *model, args []string) error {
	m.irc.Raw("LIST")
	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtSys(ts, "Fetching channel list..."))
	return nil
}

func cmdMsg(m *model, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: /msg <nick> <message>")
	}
	target := args[0]
	message := strings.Join(args[1:], " ")
	m.irc.Privmsg(target, message)

	// Add query window if not already in channels list
	if !contains(m.channels, target) {
		m.channels = append(m.channels, target)
		m.updateSidebars()
	}

	ts := time.Now().Format("15:04")
	m.addMessage(m.fmtMsg(ts, target, m.nick, message))
	return nil
}

func parseServerAddress(addr string) (server, port string) {
	addr = strings.ReplaceAll(addr, "/", ":")
	parts := strings.Split(addr, ":")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return addr, "6667"
}

func main() {
	verbose := flag.Bool("v", false, "Enable verbose/debug mode")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Println("Usage: ./irc-client [-v] <server/port> <nickname>")
		fmt.Println("Options:")
		fmt.Println("  -v    Enable verbose/debug mode (shows all IRC protocol messages)")
		fmt.Println("Examples:")
		fmt.Println("  ./irc-client irc.libera.chat/7000 myusername")
		fmt.Println("  ./irc-client irc.libera.chat:7000 myusername")
		fmt.Println("  ./irc-client -v irc.libera.chat/7000 myusername")
		os.Exit(1)
	}

	server, port := parseServerAddress(args[0])
	nick := args[1]

	p := tea.NewProgram(initialModel(server, port, nick, *verbose), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
	}
}
